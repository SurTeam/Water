package gometrics

import "sync/atomic"

var (
	PTYBytesRead atomic.Uint64
	PTYReadCalls atomic.Uint64
	PTYReadWouldBlock atomic.Uint64
	PTYReaderPollWakeups atomic.Uint64
	PTYReaderZeroEventWakeups atomic.Uint64
	PTYReaderSpuriousWakeups atomic.Uint64
	PTYReaderPTYReadyWakeups atomic.Uint64
	PTYReaderEmptyReadiness atomic.Uint64
	PTYReaderEmptyWakeBackoffs atomic.Uint64

	TerminalOutputBytesSent atomic.Uint64
	TerminalStreamEvents atomic.Uint64
	TerminalResizeEvents atomic.Uint64
	StateDumps atomic.Uint64
	ModelSnapshotPushes atomic.Uint64

	TerminalBytesReceived atomic.Uint64
	ProcessorAdvances atomic.Uint64
	TerminalBytesAdvanced atomic.Uint64
	TerminalNotifies atomic.Uint64
	TerminalRenders atomic.Uint64
	HiddenTerminalUpdates atomic.Uint64
	ReplayBytesReceived atomic.Uint64

	ReplayRingBytes atomic.Uint64

	TerminalServerQueueEvents atomic.Uint64
	TerminalServerQueueBytes atomic.Uint64
	TerminalServerQueueMaxEvents atomic.Uint64
	TerminalServerQueueMaxBytes atomic.Uint64
	TerminalClientQueueEvents atomic.Uint64
	TerminalClientQueueBytes atomic.Uint64
	TerminalClientQueueMaxEvents atomic.Uint64
	TerminalClientQueueMaxBytes atomic.Uint64
)

func ServerQueueAdd(bytes int){
	events:=TerminalServerQueueEvents.Add(1)
	current:=TerminalServerQueueBytes.Add(uint64(bytes))
	updateMax(&TerminalServerQueueMaxEvents,events)
	updateMax(&TerminalServerQueueMaxBytes,current)
}

func ServerQueueRemove(bytes int){
	TerminalServerQueueEvents.Add(^uint64(0))
	TerminalServerQueueBytes.Add(^uint64(bytes-1))
}

func ClientQueueAdd(bytes int){
	events:=TerminalClientQueueEvents.Add(1)
	current:=TerminalClientQueueBytes.Add(uint64(bytes))
	updateMax(&TerminalClientQueueMaxEvents,events)
	updateMax(&TerminalClientQueueMaxBytes,current)
}

func ClientQueueRemove(bytes int){
	TerminalClientQueueEvents.Add(^uint64(0))
	TerminalClientQueueBytes.Add(^uint64(bytes-1))
}

func updateMax(counter *atomic.Uint64,value uint64){
	for{
		current:=counter.Load()
		if value<=current{return}
		if counter.CompareAndSwap(current,value){return}
	}
}

func Snapshot()map[string]any{
	advances:=ProcessorAdvances.Load()
	advanced:=TerminalBytesAdvanced.Load()
	bytesPerAdvance:=float64(0)
	if advances!=0{bytesPerAdvance=float64(advanced)/float64(advances)}
	return map[string]any{
		"pty_bytes_read":PTYBytesRead.Load(),
		"pty_read_calls":PTYReadCalls.Load(),
		"pty_read_would_block":PTYReadWouldBlock.Load(),
		"pty_reader_poll_wakeups":PTYReaderPollWakeups.Load(),
		"pty_reader_zero_event_wakeups":PTYReaderZeroEventWakeups.Load(),
		"pty_reader_spurious_wakeups":PTYReaderSpuriousWakeups.Load(),
		"pty_reader_pty_ready_wakeups":PTYReaderPTYReadyWakeups.Load(),
		"pty_reader_empty_readiness":PTYReaderEmptyReadiness.Load(),
		"pty_reader_empty_wake_backoffs":PTYReaderEmptyWakeBackoffs.Load(),
		"terminal_output_bytes_sent":TerminalOutputBytesSent.Load(),
		"terminal_stream_events":TerminalStreamEvents.Load(),
		"terminal_resize_events":TerminalResizeEvents.Load(),
		"state_dumps":StateDumps.Load(),
		"model_snapshot_pushes":ModelSnapshotPushes.Load(),
		"replay_ring_bytes":ReplayRingBytes.Load(),
		"terminal_bytes_received":TerminalBytesReceived.Load(),
		"processor_advances":advances,
		"terminal_bytes_advanced":advanced,
		"bytes_per_advance":bytesPerAdvance,
		"terminal_notifies":TerminalNotifies.Load(),
		"terminal_renders":TerminalRenders.Load(),
		"hidden_terminal_updates":HiddenTerminalUpdates.Load(),
		"replay_bytes_received":ReplayBytesReceived.Load(),
		"terminal_server_queue_events":TerminalServerQueueEvents.Load(),
		"terminal_server_queue_bytes":TerminalServerQueueBytes.Load(),
		"terminal_server_queue_max_events":TerminalServerQueueMaxEvents.Load(),
		"terminal_server_queue_max_bytes":TerminalServerQueueMaxBytes.Load(),
		"terminal_client_queue_events":TerminalClientQueueEvents.Load(),
		"terminal_client_queue_bytes":TerminalClientQueueBytes.Load(),
		"terminal_client_queue_max_events":TerminalClientQueueMaxEvents.Load(),
		"terminal_client_queue_max_bytes":TerminalClientQueueMaxBytes.Load(),
	}
}
