package main

import (
	"fmt"
	"sync"
	"unsafe"
)

type entryHot struct {
	task             interface{}
	energy           float64
	runtimeNano      int64
	yieldCount       int
	creationPressure float64
	wake             chan struct{}
	state            uint32
	generation       uint64
}

type entry struct {
	entryHot
	_ [(128 - (unsafe.Sizeof(entryHot{}) % 128)) % 128]byte
}

func main() {
	var pool = sync.Pool{
		New: func() any {
			return &entry{}
		},
	}

	// Test a bunch of allocations
	for i := 0; i < 10000; i++ {
		e := pool.Get().(*entry)
		addr := uint64(uintptr(unsafe.Pointer(e)))
		if addr % 128 != 0 {
			fmt.Printf("NOT 128-byte aligned: %x (offset: %d)\n", addr, addr % 128)
			return
		}
	}
	fmt.Println("All 10000 allocations were exactly 128-byte aligned.")
}
