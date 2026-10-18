package util

type Well512 struct {
	State [16]uint64
	Index uint64
}

func NewWell512(seed uint64) Well512 {
	r := Well512{}
	for i := range r.State {
		r.State[i] = seed + uint64(i)*90
	}
	return r
}

func (r *Well512) Next() uint64 {
	index := r.Index
	x, y := r.State[index], r.State[(index+13)&15]
	b := x ^ y ^ (x << 16) ^ (y << 15)
	c := r.State[(index+9)&15]
	c ^= c >> 11
	x = b ^ c
	r.State[index] = x
	// Client IL zero-extends this uint32 mask into the 64-bit state.
	d := x ^ ((x << 5) & uint64(0xda442d24))
	index = (index + 15) & 15
	x = r.State[index]
	r.State[index] = x ^ b ^ d ^ (x << 2) ^ (b << 18) ^ (c << 28)
	r.Index = index
	return r.State[index]
}
