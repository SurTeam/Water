"""Owned loopback OpenSSH daemon; never modifies the system SSH service."""
import getpass
import json
import os
from pathlib import Path
import shlex
import shutil
import socket
import subprocess
import tempfile
import time
import uuid


class OpenSSHServer:
    def __init__(self, variant="dev"):
        if variant not in ("dev", "release"):
            raise ValueError("invalid test variant")
        self.variant = variant
        self.directory = Path(tempfile.mkdtemp(prefix="water-real-ssh.", dir="/tmp"))
        self.directory.chmod(0o700)
        self.alias = "water-test-" + uuid.uuid4().hex[:12]
        self.sshd = None
        self.log = None
        self.control = None
        self.remote_socket = None
        self.remote_instances = {}
        self.existing_pids = set()
        self.previous_env = {}

    def run(self, command, timeout=10, check=True):
        result = subprocess.run(["/usr/bin/ssh", "-F", str(self.directory / "ssh_config"), self.alias, command],
                                capture_output=True, text=True, timeout=timeout)
        if check and result.returncode:
            raise RuntimeError("owned SSH command failed: " + result.stderr.strip())
        return result

    def __enter__(self):
        try:
            for name in ("host", "client"):
                subprocess.run(["/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(self.directory / name)], check=True, timeout=5)
            shell = next(p for p in ("/opt/homebrew/bin/zsh", "/bin/zsh") if Path(p).is_file())
            config = self.directory / "water-config.json"
            config.write_text(json.dumps({"startup": {"default_cwd": str(self.directory)},
                                          "shell": {"program": shell, "args": ["-f"]}}))
            wrapper = self.directory / "command"
            wrapper.write_text("#!/bin/sh\n" + "printf '%s\\n' \"$SSH_ORIGINAL_COMMAND\" >> " + shlex.quote(str(self.directory / "commands.log")) + "\n" +
                               "exec env WATER_CONFIG=" + shlex.quote(str(config)) + " /bin/sh -c \"$SSH_ORIGINAL_COMMAND\"\n")
            wrapper.chmod(0o700)
            public = (self.directory / "client.pub").read_text().strip()
            (self.directory / "authorized_keys").write_text('command="' + str(wrapper) + '",no-agent-forwarding,no-X11-forwarding,no-pty ' + public + "\n")
            with socket.socket() as reservation:
                reservation.bind(("127.0.0.1", 0))
                self.port = reservation.getsockname()[1]
            host = (self.directory / "host.pub").read_text().strip()
            (self.directory / "known_hosts").write_text(f"[127.0.0.1]:{self.port} {host}\n")
            (self.directory / "sshd_config").write_text(
                f"Port {self.port}\nListenAddress 127.0.0.1\nHostKey {self.directory}/host\n"
                f"PidFile {self.directory}/sshd.pid\nAuthorizedKeysFile {self.directory}/authorized_keys\n"
                f"AllowUsers {getpass.getuser()}\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n"
                "UsePAM no\nStrictModes no\nAllowTcpForwarding yes\nAllowStreamLocalForwarding yes\nLogLevel VERBOSE\n")
            (self.directory / "ssh_config").write_text(
                f"Host {self.alias}\n HostName 127.0.0.1\n Port {self.port}\n User {getpass.getuser()}\n"
                f" IdentityFile {self.directory}/client\n IdentitiesOnly yes\n BatchMode yes\n"
                f" UserKnownHostsFile {self.directory}/known_hosts\n GlobalKnownHostsFile /dev/null\n"
                " StrictHostKeyChecking yes\n ConnectTimeout 2\n")
            subprocess.run(["/usr/sbin/sshd", "-t", "-f", str(self.directory / "sshd_config")], check=True, timeout=5)
            self.log = (self.directory / "sshd.log").open("w")
            self.sshd = subprocess.Popen(["/usr/sbin/sshd", "-D", "-e", "-f", str(self.directory / "sshd_config")],
                                         stdout=self.log, stderr=self.log, start_new_session=True)
            deadline = time.monotonic() + 5
            while True:
                result = self.run("printf REAL_SSH_AUTHENTICATED", timeout=3, check=False)
                if result.returncode == 0:
                    assert result.stdout == "REAL_SSH_AUTHENTICATED"
                    break
                if self.sshd.poll() is not None or time.monotonic() >= deadline:
                    raise RuntimeError("owned sshd did not become ready: " + result.stderr)
                time.sleep(.025)
            keys = ("WATER_SSH_PROGRAM", "WATER_SSH_CONFIG", "WATER_REMOTE_SERVER_COMMAND", "WATER_REMOTE_CONTROL_SOCKET")
            self.previous_env = {key: os.environ.get(key) for key in keys}
            for key in keys:
                os.environ.pop(key, None)
            os.environ["WATER_SSH_PROGRAM"] = "/usr/bin/ssh"
            os.environ["WATER_SSH_CONFIG"] = str(self.directory / "ssh_config")
            self.save("ownership.json", {"sshd_pid": self.sshd.pid, "alias": self.alias, "port": self.port, "directory": str(self.directory)})
            print("PASS real OpenSSH authentication; alias=" + self.alias + "; port=" + str(self.port), flush=True)
            return self
        except BaseException:
            self.close()
            raise

    def rpc(self, method):
        if not self.remote_socket:
            raise RuntimeError("no owned remote endpoint")
        code = "import socket,struct,json,sys; s=socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sys.argv[1]); p=json.dumps({'build_variant':sys.argv[3],'protocol_version':5,'request_id':1,'method':sys.argv[2]}).encode(); s.sendall(struct.pack('>I',len(p))+p); f=s.makefile('rb'); size=struct.unpack('>I',f.read(4))[0]; print(f.read(size).decode())"
        command = shlex.quote(os.sys.executable) + " -c " + shlex.quote(code) + " " + shlex.quote(self.remote_socket) + " " + shlex.quote(method) + " " + shlex.quote(self.variant)
        message = json.loads(self.run(command).stdout)
        if not message.get("ok"):
            raise RuntimeError(str(message.get("error")))
        return message.get("result")

    def save(self, name, value):
        (self.directory / name).write_text(json.dumps(value, indent=2))

    def record_remote(self, info):
        socket_path = info["socket_path"]
        # Reproduce Water's stable FNV-1a destination identity, without touching a default user socket.
        value = 14695981039346656037
        for byte in self.alias.encode():
            value = ((value ^ byte) * 1099511628211) & ((1 << 64) - 1)
        expected = f"/tmp/water-go-{self.variant}-{value:016x}.sock"
        if socket_path != expected or info["build_variant"] != self.variant or info["server_pid"] in self.existing_pids:
            raise RuntimeError("remote server does not belong to the owned SSH destination")
        args = self.run("ps -p " + str(info["server_pid"]) + " -o command=").stdout
        if "--socket " + expected not in args:
            raise RuntimeError("remote server process arguments do not match the owned endpoint")
        self.remote_socket = expected
        self.remote_instances[info["instance_id"]] = info["server_pid"]
        self.save("remote-instances.json", self.remote_instances)

    def __exit__(self, kind, error, traceback):
        if error:
            self.save("failure.json", {"error": str(error)})
        self.close()

    def close(self):
        remote_removed = True
        master_removed = True
        cleanup_error = None
        try:
            if self.remote_socket and self.sshd and self.sshd.poll() is None:
                info = self.rpc("server.inspect")
                self.record_remote(info)
                self.rpc("server.shutdown")
                deadline = time.monotonic() + 5
                while self.run("test ! -S " + shlex.quote(self.remote_socket), check=False).returncode:
                    if time.monotonic() >= deadline:
                        raise RuntimeError("owned remote server did not remove its socket")
                    time.sleep(.025)
        except BaseException as error:
            remote_removed = False
            cleanup_error = error
        try:
            if self.control and self.sshd and self.sshd.poll() is None:
                subprocess.run(["/usr/bin/ssh", "-F", str(self.directory / "ssh_config"), "-S", self.control,
                                "-O", "exit", self.alias], capture_output=True, text=True, timeout=5)
                master_removed = not Path(self.control).exists()
        except BaseException as error:
            master_removed = False
            cleanup_error = cleanup_error or error
        finally:
            if self.sshd and self.sshd.poll() is None:
                self.sshd.terminate()
                try:
                    self.sshd.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.sshd.kill()  # Only the Popen-owned test daemon.
                    self.sshd.wait(timeout=3)
            if self.log:
                self.log.close()
            for key, value in self.previous_env.items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value
            for name in ("host", "client"):
                (self.directory / name).unlink(missing_ok=True)
            self.save("sshd-cleanup.json", {"sshd_exited": not self.sshd or self.sshd.poll() is not None,
                                           "master_removed": master_removed, "remote_socket_removed": remote_removed,
                                           "private_keys_removed": True,
                                           "error": str(cleanup_error) if cleanup_error else None})
            export = os.environ.get("WATER_TEST_EVIDENCE_DIR")
            if export:
                shutil.copytree(self.directory, Path(export) / self.directory.name, dirs_exist_ok=True)
        if cleanup_error:
            raise cleanup_error
        if not master_removed:
            raise RuntimeError("owned SSH master did not close")
