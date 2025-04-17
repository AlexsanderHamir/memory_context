package contexts

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"memctx/pool"
)

type MemoryContext struct {
	active         bool
	closed         bool
	referenceCount int32
	mu             sync.RWMutex

	contextType MemoryContextType
	parent      *MemoryContext
	children    []*MemoryContext
	pools       map[reflect.Type]*pool.Pool[any]
	createdAt   time.Time
	lastUsed    time.Time
}

type MemoryContextConfig struct {
	Parent      *MemoryContext
	ContextType MemoryContextType
}

func NewMemoryContext(config MemoryContextConfig) *MemoryContext {
	now := time.Now()
	mc := &MemoryContext{
		parent:      config.Parent,
		contextType: config.ContextType,
		pools:       make(map[reflect.Type]*pool.Pool[any]),
		createdAt:   now,
		lastUsed:    now,
	}

	return mc
}

// Creates and registers a child context with the same
// context type as the parent.
func (mc *MemoryContext) CreateChild() error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.closed {
		return ErrContextClosed
	}

	memCtx := mc.allocate()
	mc.RegisterChild(memCtx)

	return nil
}

func (mc *MemoryContext) allocate() *MemoryContext {
	return NewMemoryContext(MemoryContextConfig{
		Parent:      mc,
		ContextType: mc.contextType,
	})
}

// Register a child with custom contex type, not the same
// as its parent
func (mc *MemoryContext) RegisterChild(child *MemoryContext) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.children = append(mc.children, child)
}

// Object Methods
// checking github username update
func (mc *MemoryContext) CreatePool(objectType reflect.Type, config pool.PoolConfig, allocator func() any, cleaner func(any)) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.closed {
		return ErrContextClosed
	}

	poolConfig := pool.ToInternalConfig(config)
	poolObj, err := pool.NewPool(poolConfig, allocator, cleaner, objectType)
	if err != nil {
		return fmt.Errorf("NewPool failed: %w", err)
	}

	mc.pools[objectType] = poolObj
	return nil
}

func (mm *MemoryContext) Acquire(objectType reflect.Type) any {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	if mm.closed {
		return nil
	}

	poolObj, exists := mm.pools[objectType]
	if !exists {
		return nil
	}

	obj := poolObj.Get()
	return obj
}

func (mm *MemoryContext) Release(objectType reflect.Type, obj any) bool {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	if mm.closed {
		return false
	}

	poolObj, exists := mm.pools[objectType]
	if !exists {
		return false
	}

	if reflect.TypeOf(obj) != objectType {
		return false
	}

	poolObj.Put(obj)
	return true
}

func (mm *MemoryContext) GetPool(objectType reflect.Type) *pool.Pool[any] {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	poolObj, exists := mm.pools[objectType]
	if !exists {
		return nil
	}

	return poolObj
}

func (mc *MemoryContext) Close() error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.closed {
		return nil
	}

	for _, pool := range mc.pools {
		if err := pool.Close(); err != nil {
			return fmt.Errorf("failed to close pool: %w", err)
		}
	}

	for _, child := range mc.children {
		if err := child.Close(); err != nil {
			return fmt.Errorf("failed to close child context: %w", err)
		}
	}

	mc.pools = nil
	mc.children = nil
	mc.closed = true
	mc.active = false
	mc.referenceCount = 0

	return nil
}
