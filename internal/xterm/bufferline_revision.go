package xterm

// Revision identifies cell mutations on this line. Readers must hold the
// terminal owner's lock; wrapping metadata is compared separately.
func (bl *BufferLine) Revision() uint64 { return bl.revision }
