// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// TestPCacheMethods2Layout pins the field-name surface of the
// sqlite3_pcache_methods2 struct as cznic transpiles it. populateCMethods
// assigns by named field, so a renamed field would silently leave a C
// pointer slot null and crash on first use. The total struct size is
// also asserted as a lower bound to catch a regenerator that drops a
// field outright.
//
// The test deliberately does NOT pin the field count: at least one
// supported arch (netbsd_amd64) emits a 14-field layout with an explicit
// F__ccgo_pad1 padding member, and counting fields would force an
// arch-conditional assertion. Named-field presence catches the failure
// modes we care about without that friction.
func TestPCacheMethods2Layout(t *testing.T) {
	var m pcacheMethods2

	wantKinds := map[string]reflect.Kind{
		"FiVersion":   reflect.Int32,
		"FpArg":       reflect.Uintptr,
		"FxInit":      reflect.Uintptr,
		"FxShutdown":  reflect.Uintptr,
		"FxCreate":    reflect.Uintptr,
		"FxCachesize": reflect.Uintptr,
		"FxPagecount": reflect.Uintptr,
		"FxFetch":     reflect.Uintptr,
		"FxUnpin":     reflect.Uintptr,
		"FxRekey":     reflect.Uintptr,
		"FxTruncate":  reflect.Uintptr,
		"FxDestroy":   reflect.Uintptr,
		"FxShrink":    reflect.Uintptr,
	}
	tp := reflect.TypeOf(m)
	for name, kind := range wantKinds {
		f, ok := tp.FieldByName(name)
		assert.True(t, ok)

		assert.Equal(t, kind, f.Type.Kind())

	}

	minSize := unsafe.Sizeof(int32(0)) + 12*unsafe.Sizeof(uintptr(0))
	require.GreaterOrEqual(t, unsafe.Sizeof(m), minSize)

	off := func(name string) uintptr {
		f, _ := tp.FieldByName(name)
		return f.Offset
	}
	assert.Greater(t, (off("FpArg") > off("FiVersion")))

	assert.Greater(t, (off("FxInit") > off("FpArg")))

	assert.Greater(t, (off("FxShrink") > off("FxDestroy")))

}

// TestRegisterPageCacheNil covers the nil-module rejection path.
// Pure input validation, does not touch pcacheState beyond reading
// nothing, safe to run in any order with other tests.
func TestRegisterPageCacheNil(t *testing.T) {
	err := RegisterPageCache(nil)
	require.False(t, err == nil || !strings.Contains(err.Error(), "RegisterPageCache(nil)"))

}

// noopModule is a PageCache whose factory returns a noopCache
// that refuses all Fetch and pretends to be a working cache. It is used
// by lifecycle tests that need a non-nil module to register but do not
// open a database and therefore never exercise the cache callbacks.
type noopModule struct{}

func (noopModule) Create(int, int, bool) (Cache, error) { return noopCache{}, nil }

type noopCache struct{}

func (noopCache) SetSize(int)                  {}
func (noopCache) PageCount() int               { return 0 }
func (noopCache) Fetch(uint32, FetchMode) Page { return nil }
func (noopCache) Unpin(Page, bool)             {}
func (noopCache) Rekey(Page, uint32, uint32)   {}
func (noopCache) Truncate(uint32)              {}
func (noopCache) Destroy()                     {}
func (noopCache) Shrink()                      {}

// TestRegisterPageCacheLifecycle exercises the gate ordering
// between RegisterPageCache and the Open path. The subtests share
// pcacheState and must run before any sql.Open call in the same
// process; they manipulate the gate flags directly so no real
// sqlite3_config call commits during the test.
func TestRegisterPageCacheLifecycle(t *testing.T) {
	pcacheState.openGate.RLock()
	registered := pcacheState.registered
	opened := pcacheState.opened.Load()
	pcacheState.openGate.RUnlock()
	if registered != nil || opened {
		t.Skip("pcacheState already polluted by an earlier test in this run; " +
			"lifecycle test must run before any sql.Open call")
	}

	t.Run("DifferentValuesConflict", func(t *testing.T) {
		// PageCache interface comparison uses underlying value
		// equality. Two distinct *noopModulePtr addresses compare
		// unequal; two struct-typed noopModule{} values with no fields
		// compare equal, so we use pointer receivers here to model
		// "two different impls registered". We bypass configOnce by
		// writing to registered directly so the test does not commit
		// a real install.
		pm1 := &noopModulePtr{}
		pm2 := &noopModulePtr{}

		pcacheState.openGate.Lock()
		if pcacheState.registered != nil {
			pcacheState.openGate.Unlock()
			t.Skip("registered slot filled before subtest")
		}
		pcacheState.registered = pm1
		pcacheState.openGate.Unlock()
		defer func() {
			pcacheState.openGate.Lock()
			pcacheState.registered = nil
			pcacheState.openGate.Unlock()
		}()

		assert.NoError(t, RegisterPageCache(pm1))

		err := RegisterPageCache(pm2)
		assert.True(t, errors.Is(err, ErrPageCacheConflict))

	})

	t.Run("TooLate", func(t *testing.T) {
		pcacheState.openGate.Lock()
		if pcacheState.registered != nil {
			pcacheState.openGate.Unlock()
			t.Skip("registered slot filled before subtest")
		}
		prevOpened := pcacheState.opened.Load()
		pcacheState.opened.Store(true)
		pcacheState.openGate.Unlock()
		defer func() {
			pcacheState.openGate.Lock()
			pcacheState.opened.Store(prevOpened)
			pcacheState.openGate.Unlock()
		}()

		err := RegisterPageCache(noopModule{})
		assert.True(t, errors.Is(err, ErrPageCacheTooLate))

	})

	t.Run("TooLateIdempotentForSameValue", func(t *testing.T) {
		m := &noopModulePtr{}
		pcacheState.openGate.Lock()
		if pcacheState.registered != nil {
			pcacheState.openGate.Unlock()
			t.Skip("registered slot filled before subtest")
		}
		pcacheState.registered = m
		prevOpened := pcacheState.opened.Load()
		pcacheState.opened.Store(true)
		pcacheState.openGate.Unlock()
		defer func() {
			pcacheState.openGate.Lock()
			pcacheState.registered = nil
			pcacheState.opened.Store(prevOpened)
			pcacheState.openGate.Unlock()
		}()

		assert.NoError(t, RegisterPageCache(m))

	})
}

// noopModulePtr has a token field so two &noopModulePtr{} composite
// literals are guaranteed distinct addresses. Zero-size structs may
// legally share addresses under the Go specification, which would
// silently break the DifferentValuesConflict subtest.
type noopModulePtr struct{ _ uint8 }

func (*noopModulePtr) Create(int, int, bool) (Cache, error) { return noopCache{}, nil }

// TestOpenGateConcurrentReaders is a smoke test that the openGate
// RWMutex does not deadlock under a fan of concurrent Open-side
// readers. It does not exercise the Register-vs-Open race: a real
// Register call would commit Xsqlite3_config, which is process-global
// and would break every other test in the package. The Register-vs-Open
// ordering is exercised indirectly by
// TestRegisterPageCacheLifecycle/TooLate, which sets the opened
// flag and verifies Register sees it.
func TestOpenGateConcurrentReaders(t *testing.T) {
	const iterations = 32
	const openers = 16
	var wg sync.WaitGroup
	wg.Add(openers)

	for i := 0; i < openers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = withOpenGate(func() error { return nil })
			}
		}()
	}
	wg.Wait()

	require.True(t, pcacheState.opened.Load())

	done := make(chan struct{})
	go func() {
		pcacheState.openGate.Lock()
		pcacheState.openGate.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Lock did not acquire within 5s after RLock fan-out; gate is stuck")
	}
}

// TestPCacheIDGen exercises the bitset-backed ID allocator used by the
// trampoline registry. The allocator is internal so the test reaches
// for it directly; it is here rather than in a separate file so the
// pcache feature stays self-contained.
func TestPCacheIDGen(t *testing.T) {
	var g pcacheIDGen
	id1 := g.next()
	id2 := g.next()
	id3 := g.next()
	require.False(t, id1 == 0 || id2 == 0 || id3 == 0)

	require.False(t, id1 == id2 || id1 == id3 || id2 == id3)

	g.reclaim(id2)
	id4 := g.next()
	assert.Equal(t, id2, id4)

}

// An end-to-end test that actually opens a sqlite database and routes
// it through a custom PageCache cannot live in this package: the
// github.com/wow-look-at-my/go-sqlite/vec package's init() calls
// Xsqlite3_auto_extension, which itself calls Xsqlite3_initialize
// (lib/sqlite_darwin_arm64.go: Xsqlite3_auto_extension), and vec is
// loaded by vec_test.go in this same test binary. By the time any test
// here runs, sqlite3_initialize has already fired and
// Xsqlite3_config(SQLITE_CONFIG_PCACHE2) returns SQLITE_MISUSE. The
// wiring has been exercised end-to-end by the maintainer with a
// minimal page cache (see MR thread); we defer adding an in-tree
// end-to-end test to a follow-up MR that can isolate the binary from
// vec's init path.
