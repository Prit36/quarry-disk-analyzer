package index

import "sync"

var (
	uint64Pool = sync.Pool{New: func() any { return new([]uint64) }}
	idPool     = sync.Pool{New: func() any { return new([]ID) }}
	uint32Pool = sync.Pool{New: func() any { return new([]uint32) }}
)

func getUint64s(n int) (*[]uint64, []uint64) {
	p := uint64Pool.Get().(*[]uint64)
	if cap(*p) < n {
		*p = make([]uint64, n)
	} else {
		*p = (*p)[:n]
	}
	return p, *p
}

func putUint64s(p *[]uint64) {
	if cap(*p) <= 1<<22 {
		uint64Pool.Put(p)
	}
}

func getIDs(n int) (*[]ID, []ID) {
	p := idPool.Get().(*[]ID)
	if cap(*p) < n {
		*p = make([]ID, n)
	} else {
		*p = (*p)[:n]
	}
	return p, *p
}

func putIDs(p *[]ID) {
	if cap(*p) <= 1<<22 {
		idPool.Put(p)
	}
}

func getUint32s(n int) (*[]uint32, []uint32) {
	p := uint32Pool.Get().(*[]uint32)
	if cap(*p) < n {
		*p = make([]uint32, n)
	} else {
		*p = (*p)[:n]
		clear(*p)
	}
	return p, *p
}

func putUint32s(p *[]uint32) {
	if cap(*p) <= 1<<22 {
		uint32Pool.Put(p)
	}
}

func GetUint32s(n int) (*[]uint32, []uint32) { return getUint32s(n) }
func PutUint32s(p *[]uint32)                 { putUint32s(p) }

var wordKeyPool = sync.Pool{New: func() any { return new([]wordKey) }}

func getWordKeys(n int) (*[]wordKey, []wordKey) {
	p := wordKeyPool.Get().(*[]wordKey)
	if cap(*p) < n {
		*p = make([]wordKey, n)
	} else {
		*p = (*p)[:n]
	}
	return p, *p
}

func putWordKeys(p *[]wordKey) {
	if cap(*p) <= 1<<22 {
		wordKeyPool.Put(p)
	}
}
