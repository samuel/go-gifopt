package lzwopt

// BitStream accumulates codes as a sequence of bits, LSB-first to match GIF's bit
// order.
type BitStream struct {
	buf   []byte
	nBits int
}

// AppendCode appends the low numBits bits of code, LSB first.
func (b *BitStream) AppendCode(code uint, numBits int) {
	needed := (b.nBits + numBits + 7) >> 3
	if needed > len(b.buf) {
		b.buf = append(b.buf, make([]byte, needed-len(b.buf))...)
	}
	byteOff := b.nBits >> 3
	bitOff := uint(b.nBits & 7)
	v := code << bitOff
	b.buf[byteOff] |= byte(v)
	if numBits+int(bitOff) > 8 {
		b.buf[byteOff+1] |= byte(v >> 8)
	}
	if numBits+int(bitOff) > 16 {
		b.buf[byteOff+2] |= byte(v >> 16)
	}
	b.nBits += numBits
}

// Len returns the number of bits.
func (b *BitStream) Len() int {
	return b.nBits
}

// ToGIFSubBlocks wraps the packed bytes in GIF sub-blocks: each at most 255 bytes,
// prefixed by its length, and terminated by a zero byte.
func (b *BitStream) ToGIFSubBlocks() []byte {
	numBytes := (b.nBits + 7) >> 3
	const maxBlock = 255
	out := make([]byte, 0, numBytes+numBytes/maxBlock+2)
	src := b.buf[:numBytes]
	for len(src) > 0 {
		n := min(len(src), maxBlock)
		out = append(out, byte(n))
		out = append(out, src[:n]...)
		src = src[n:]
	}
	out = append(out, 0x00) // block terminator
	return out
}
