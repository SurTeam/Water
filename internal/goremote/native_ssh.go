package goremote

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SurTeam/Water/internal/gobridge"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type nativeSSH struct {
	client  *ssh.Client
	parents []*ssh.Client
}

func (n *nativeSSH) close() {
	n.client.Close()
	for _, parent := range n.parents {
		parent.Close()
	}
}

type nativeSSHConfig struct {
	host, port, user string
	values           map[string]string
	identities       []string
	home             string
}

func configWords(line string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range line {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '#':
			flush()
			return words
		case ' ', '\t', '=':
			flush()
		default:
			word.WriteRune(r)
		}
	}
	flush()
	return words
}
func hostPattern(pattern, host string) bool {
	ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(host))
	return ok
}
func (c *nativeSSHConfig) read(file, alias string, depth int) error {
	if depth > 8 {
		return errors.New("SSH config Include nesting exceeds limit")
	}
	input, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer input.Close()
	active := true
	scan := bufio.NewScanner(input)
	for scan.Scan() {
		words := configWords(scan.Text())
		if len(words) < 2 {
			continue
		}
		name := strings.ToLower(words[0])
		values := words[1:]
		if name == "host" {
			active = false
			excluded := false
			for _, pattern := range values {
				if strings.HasPrefix(pattern, "!") {
					if hostPattern(pattern[1:], alias) {
						excluded = true
					}
				} else if hostPattern(pattern, alias) {
					active = true
				}
			}
			if excluded {
				active = false
			}
			continue
		}
		if name == "match" {
			return errors.New("native SSH config Match blocks are not supported; use Host blocks")
		}
		if !active {
			continue
		}
		if name == "include" {
			for _, pattern := range values {
				pattern = c.expand(pattern)
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(filepath.Dir(file), pattern)
				}
				files, _ := filepath.Glob(pattern)
				for _, included := range files {
					if err := c.read(included, alias, depth+1); err != nil {
						return err
					}
				}
			}
			continue
		}
		if name == "identityfile" {
			c.identities = append(c.identities, strings.Join(values, " "))
			continue
		}
		if _, exists := c.values[name]; !exists {
			c.values[name] = strings.Join(values, " ")
		}
	}
	return scan.Err()
}
func (c *nativeSSHConfig) expand(value string) string {
	if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		value = filepath.Join(c.home, value[2:])
	}
	replace := strings.NewReplacer("%d", c.home, "%h", c.host, "%r", c.user, "%p", c.port)
	return replace.Replace(value)
}
func loadNativeConfig(destination string) (*nativeSSHConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	alias := destination
	explicitUser := ""
	if at := strings.LastIndex(destination, "@"); at >= 0 {
		explicitUser = destination[:at]
		alias = destination[at+1:]
	}
	c := &nativeSSHConfig{host: alias, port: "22", home: home, values: map[string]string{}}
	config := os.Getenv("WATER_SSH_CONFIG")
	if config == "" {
		config = filepath.Join(home, ".ssh", "config")
	}
	if err := c.read(config, alias, 0); err != nil {
		return nil, err
	}
	if host := c.values["hostname"]; host != "" {
		c.host = host
	}
	if port := c.values["port"]; port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("invalid SSH Port")
		}
		c.port = port
	}
	c.user = explicitUser
	if c.user == "" {
		c.user = c.values["user"]
	}
	if c.user == "" {
		if current, err := user.Current(); err == nil {
			c.user = current.Username
			if slash := strings.LastIndexAny(c.user, `/\`); slash >= 0 {
				c.user = c.user[slash+1:]
			}
		}
	}
	if proxy := c.values["proxycommand"]; proxy != "" && proxy != "none" {
		return nil, errors.New("native Windows SSH supports ProxyJump, but not ProxyCommand")
	}
	if len(c.identities) == 0 {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			c.identities = append(c.identities, filepath.Join(home, ".ssh", name))
		}
	}
	for i, identity := range c.identities {
		c.identities[i] = c.expand(identity)
	}
	return c, nil
}
func (c *nativeSSHConfig) hostKeyCallback(prompt Prompt) (ssh.HostKeyCallback, error) {
	files := []string{filepath.Join(c.home, ".ssh", "known_hosts")}
	if configured := c.values["userknownhostsfile"]; configured != "" {
		files = configWords(configured)
		for i, file := range files {
			files[i] = c.expand(file)
		}
	}
	var present []string
	for _, file := range files {
		if _, err := os.Stat(file); err == nil {
			present = append(present, file)
		}
	}
	var verify ssh.HostKeyCallback
	if len(present) > 0 {
		var err error
		verify, err = knownhosts.New(present...)
		if err != nil {
			return nil, err
		}
	}
	strict := strings.ToLower(c.values["stricthostkeychecking"])
	if strict == "no" || strict == "off" {
		return ssh.InsecureIgnoreHostKey(), nil
	} // explicit user configuration
	var mu sync.Mutex
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()
		if alias := c.values["hostkeyalias"]; alias != "" {
			host = net.JoinHostPort(alias, c.port)
		}
		var err error
		if verify != nil {
			err = verify(host, remote, key)
			if err == nil {
				return nil
			}
		} else {
			err = &knownhosts.KeyError{}
		}
		var unknown *knownhosts.KeyError
		if !errors.As(err, &unknown) || len(unknown.Want) > 0 {
			return fmt.Errorf("SSH host key verification failed: %w", err)
		}
		accept := strict == "accept-new"
		if !accept && prompt != nil && (strict == "" || strict == "ask") {
			reply, e := prompt(fmt.Sprintf("Accept SSH host %s key %s? Type yes to continue.", host, ssh.FingerprintSHA256(key)))
			if e != nil {
				return e
			}
			accept = strings.EqualFold(strings.TrimSpace(reply), "yes")
		}
		if !accept {
			return fmt.Errorf("unknown SSH host key for %s (%s); configure a known_hosts entry or StrictHostKeyChecking accept-new", host, ssh.FingerprintSHA256(key))
		}
		if len(files) == 0 {
			return errors.New("no UserKnownHostsFile available")
		}
		if err := os.MkdirAll(filepath.Dir(files[0]), 0700); err != nil {
			return err
		}
		file, e := os.OpenFile(files[0], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(file, knownhosts.Line([]string{host}, key))
		closeErr := file.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		verify, e = knownhosts.New(files[0])
		return e
	}, nil
}
func dialNativeSSH(destination string, prompt Prompt, depth int) (*nativeSSH, error) {
	if depth > 4 {
		return nil, errors.New("SSH ProxyJump nesting exceeds limit")
	}
	c, err := loadNativeConfig(destination)
	if err != nil {
		return nil, err
	}
	callback, err := c.hostKeyCallback(prompt)
	if err != nil {
		return nil, err
	}
	var signers []ssh.Signer
	for _, file := range c.identities {
		if file == "none" {
			continue
		}
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		signer, err := ssh.ParsePrivateKey(data)
		var encrypted *ssh.PassphraseMissingError
		if errors.As(err, &encrypted) && prompt != nil {
			passphrase, e := prompt("Passphrase for SSH key " + file + ":")
			if e != nil {
				return nil, e
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
		}
		if err != nil {
			return nil, fmt.Errorf("read SSH identity %s: %w", file, err)
		}
		signers = append(signers, signer)
	}
	// Agent keys stay in the agent; no secret is copied into arguments or env.
	if c.values["identitiesonly"] != "yes" {
		if connection, err := openNativeAgent(); err == nil {
			defer connection.Close()
			keys, err := agent.NewClient(connection).Signers()
			if err == nil {
				signers = append(signers, keys...)
			}
		}
	}
	auth := []ssh.AuthMethod{}
	if len(signers) > 0 {
		auth = append(auth, ssh.PublicKeys(signers...))
	}
	if prompt != nil {
		auth = append(auth, ssh.KeyboardInteractive(func(user, instruction string, questions []string, echo []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i, question := range questions {
				answer, err := prompt(question)
				if err != nil {
					return nil, err
				}
				answers[i] = answer
			}
			return answers, nil
		}), ssh.PasswordCallback(func() (string, error) { return prompt(c.user + "@" + c.host + " password:") }))
	}
	configuration := &ssh.ClientConfig{User: c.user, Auth: auth, HostKeyCallback: callback, HostKeyAlgorithms: c.knownHostAlgorithms(), Timeout: 10 * time.Second}
	address := net.JoinHostPort(strings.Trim(c.host, "[]"), c.port)
	var connection net.Conn
	var parents []*ssh.Client
	if jump := c.values["proxyjump"]; jump != "" && jump != "none" {
		if strings.Contains(jump, ",") {
			return nil, errors.New("use nested Host ProxyJump settings for multiple jump hosts")
		}
		parent, err := dialNativeSSH(jump, prompt, depth+1)
		if err != nil {
			return nil, err
		}
		parents = append(parents, parent.client)
		parents = append(parents, parent.parents...)
		connection, err = parent.client.Dial("tcp", address)
		if err != nil {
			parent.close()
			return nil, err
		}
	} else {
		connection, err = net.DialTimeout("tcp", address, 10*time.Second)
		if err != nil {
			return nil, err
		}
	}
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	transport, channels, requests, err := ssh.NewClientConn(connection, address, configuration)
	if err != nil {
		connection.Close()
		for _, parent := range parents {
			parent.Close()
		}
		return nil, fmt.Errorf("SSH authentication/handshake: %w", err)
	}
	_ = connection.SetDeadline(time.Time{})
	result := &nativeSSH{client: ssh.NewClient(transport, channels, requests), parents: parents}
	go result.keepAlive()
	return result, nil
}
func (n *nativeSSH) output(script string, input io.Reader, timeout time.Duration) ([]byte, []byte, error) {
	session, err := n.client.NewSession()
	if err != nil {
		return nil, nil, err
	}
	defer session.Close()
	var out, detail bytes.Buffer
	session.Stdout = &out
	session.Stderr = &detail
	session.Stdin = input
	timer := time.AfterFunc(timeout, func() { session.Close() })
	defer timer.Stop()
	err = session.Run(script)
	return out.Bytes(), detail.Bytes(), err
}
func (n *nativeSSH) upload(payload []byte, remote string) error {
	client, err := sftp.NewClient(n.client)
	if err != nil {
		return err
	}
	defer client.Close()
	timer := time.AfterFunc(2*time.Minute, func() { client.Close() })
	defer timer.Stop()
	file, err := client.Create(strings.ReplaceAll(remote, `\`, "/"))
	if err != nil {
		return err
	}
	_, err = io.Copy(file, bytes.NewReader(payload))
	closed := file.Close()
	if err != nil {
		return err
	}
	return closed
}

type sshSessionReader struct {
	io.Reader
	session *ssh.Session
}

func (r *sshSessionReader) Close() error { return r.session.Close() }
func (p *portableTunnel) serveNative(connection net.Conn, remote string) {
	session, err := p.native.client.NewSession()
	if err != nil {
		connection.Close()
		return
	}
	defer session.Close()
	input, err := session.StdoutPipe()
	if err != nil {
		connection.Close()
		return
	}
	output, err := session.StdinPipe()
	if err != nil {
		connection.Close()
		return
	}
	diagnostic := &boundedDiagnostic{}
	session.Stderr = diagnostic
	p.mu.Lock()
	p.lastDiagnostic = diagnostic
	p.mu.Unlock()
	if err = session.Start(p.invoke("--stdio-bridge", "--socket", remote)); err != nil {
		connection.Close()
		return
	}
	_ = gobridge.Relay(connection, &sshSessionReader{input, session}, output)
}

// Match OpenSSH's preference for an already trusted host-key type. Otherwise
// x/crypto may negotiate ECDSA while known_hosts pins this host's Ed25519 key.
func (c *nativeSSHConfig) knownHostAlgorithms() []string {
	files := []string{filepath.Join(c.home, ".ssh", "known_hosts")}
	if value := c.values["userknownhostsfile"]; value != "" {
		files = configWords(value)
		for i, file := range files {
			files[i] = c.expand(file)
		}
	}
	var present []string
	for _, file := range files {
		if _, err := os.Stat(file); err == nil {
			present = append(present, file)
		}
	}
	if len(present) == 0 {
		return nil
	}
	verify, err := knownhosts.New(present...)
	if err != nil {
		return nil
	}
	dummy, _ := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	address := net.JoinHostPort(c.host, c.port)
	if alias := c.values["hostkeyalias"]; alias != "" {
		address = net.JoinHostPort(alias, c.port)
	}
	var mismatch *knownhosts.KeyError
	err = verify(address, &net.TCPAddr{IP: net.ParseIP(c.host)}, dummy)
	if !errors.As(err, &mismatch) {
		return nil
	}
	var algorithms []string
	seen := map[string]bool{}
	for _, trusted := range mismatch.Want {
		names := []string{trusted.Key.Type()}
		if trusted.Key.Type() == ssh.KeyAlgoRSA {
			names = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		}
		for _, name := range names {
			if !seen[name] {
				algorithms = append(algorithms, name)
				seen[name] = true
			}
		}
	}
	return algorithms
}

func (n *nativeSSH) keepAlive() {
	closed := make(chan struct{})
	go func() { _ = n.client.Wait(); close(closed) }()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-closed:
			return
		case <-ticker.C:
			reply := make(chan error, 1)
			go func() { _, _, err := n.client.SendRequest("keepalive@openssh.com", true, nil); reply <- err }()
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-closed:
				timer.Stop()
				return
			case err := <-reply:
				timer.Stop()
				if err != nil {
					n.close()
					return
				}
			case <-timer.C:
				n.close()
				return
			}
		}
	}
}
