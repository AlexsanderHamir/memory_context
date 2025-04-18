package contexts

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrContextNotFound      = errors.New("context not found")
	ErrContextInUse         = errors.New("context is currently in use")
	ErrInvalidContextType   = errors.New("invalid context type")
	ErrContextClosed        = errors.New("context is closed")
	ErrContextManagerClosed = errors.New("context manager is closed")
	ErrMaxReferencesReached = errors.New("maximum references reached")
	ErrInvalidConfig        = errors.New("invalid configuration")
)

const (
	DefaultMaxReferences   = 10
	DefaultMaxIdleTime     = 5 * time.Minute
	DefaultCleanupInterval = 1 * time.Minute
	MinMaxReferences       = 1
	MinMaxIdleTime         = 1 * time.Second
	MinCleanupInterval     = 1 * time.Second
)

type MemoryContextType string

type ContextManager struct {
	mu       *sync.RWMutex
	ctxCache map[MemoryContextType]*MemoryContext
	config   *ContextConfig
	stopChan chan struct{}
	closed   bool
}

type ContextConfig struct {
	// how many goroutines can use the context at the same time.
	MaxReferences int32

	// how long the context can be idle before it is automatically cleaned up.
	MaxIdleTime time.Duration

	// the interval at which the context will be cleaned up when it is not in use.
	CleanupInterval time.Duration
}

// NewDefaultConfig creates a new ContextConfig with default values
func newDefaultConfig() *ContextConfig {
	return &ContextConfig{
		MaxReferences:   DefaultMaxReferences,
		MaxIdleTime:     DefaultMaxIdleTime,
		CleanupInterval: DefaultCleanupInterval,
	}
}

// NewContextManager creates a new context manager with default configuration
func NewContextManager(config *ContextConfig) *ContextManager {
	if config == nil {
		config = newDefaultConfig()
	}

	cm := &ContextManager{
		mu:       &sync.RWMutex{},
		ctxCache: make(map[MemoryContextType]*MemoryContext),
		config:   config,
		stopChan: make(chan struct{}),
	}

	if config.CleanupInterval > 0 {
		go cm.startCleanupRoutine()
	}

	return cm
}

// startCleanupRoutine runs a background goroutine to clean up idle contexts
func (cm *ContextManager) startCleanupRoutine() {
	ticker := time.NewTicker(cm.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cm.cleanupIdleContexts()
		case <-cm.stopChan:
			return
		}
	}
}

// cleanupIdleContexts removes contexts that have been idle for too long
func (cm *ContextManager) cleanupIdleContexts() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for ctxType, ctx := range cm.ctxCache {
		if !ctx.active && time.Since(ctx.lastUsed) > cm.config.MaxIdleTime {
			if err := cm.CloseContext(ctxType, true); err != nil {
				fmt.Printf("Error closing idle context %s: %v\n", ctxType, err)
			}
		}
	}
}

// StopCleanup stops the auto-cleanup routine
func (cm *ContextManager) StopCleanup() {
	close(cm.stopChan)
}

// getOrCreateContext is the internal version of GetOrCreateContext
func (cm *ContextManager) GetOrCreateContext(ctxType MemoryContextType, config MemoryContextConfig) (*MemoryContext, bool, error) {
	if ctxType == "" {
		return nil, false, ErrInvalidContextType
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	ctx, ok := cm.ctxCache[ctxType]
	if !ok {
		ctx = NewMemoryContext(config)
		ctx.active = true
		ctx.referenceCount = 1
		cm.ctxCache[ctxType] = ctx
		return ctx, false, nil
	}

	if ctx.closed {
		return nil, false, ErrContextClosed
	}

	// Check if we've reached the maximum number of references
	maxRefEnabled := cm.config.MaxReferences > 0
	if maxRefEnabled && ctx.referenceCount >= cm.config.MaxReferences {
		return nil, false, ErrMaxReferencesReached
	}

	ctx.referenceCount++

	return ctx, true, nil
}

// ReturnContext returns a context to the manager and updates its state
func (cm *ContextManager) ReturnContext(ctx *MemoryContext) error {
	if ctx == nil {
		return errors.New("cannot return nil context")
	}

	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	if ctx.closed {
		return ErrContextClosed
	}

	if ctx.referenceCount > 0 {
		ctx.referenceCount--
	}

	ctx.active = ctx.referenceCount > 0

	return nil
}

// CloseContext closes a context and removes it from the manager
func (cm *ContextManager) CloseContext(ctxType MemoryContextType, locked bool) error {
	if !locked {
		cm.mu.Lock()
		defer cm.mu.Unlock()
	}

	ctx, ok := cm.ctxCache[ctxType]
	if !ok {
		return ErrContextNotFound
	}

	ctx.mu.RLock()
	if ctx.referenceCount > 0 {
		ctx.mu.RUnlock()
		return ErrContextInUse
	}
	ctx.mu.RUnlock()

	if err := ctx.Close(); err != nil {
		return fmt.Errorf("error closing context %s: %v", ctxType, err)
	}

	delete(cm.ctxCache, ctxType)

	return nil
}

// ValidateContext checks if a context is valid and active
func (cm *ContextManager) ValidateContext(ctx *MemoryContext) bool {
	if ctx == nil {
		return false
	}

	ctx.mu.RLock()
	defer ctx.mu.RUnlock()

	return !ctx.closed && ctx.active
}

// Close stops the cleanup routine and closes all managed contexts
func (cm *ContextManager) Close() error {
	cm.mu.RLock()

	if cm.closed {
		cm.mu.RUnlock()
		return ErrContextManagerClosed
	}

	if cm.config.CleanupInterval > 0 {
		cm.StopCleanup()
	}

	cm.mu.RUnlock()
	for ctxType := range cm.ctxCache {
		if err := cm.CloseContext(ctxType, false); err != nil {
			return fmt.Errorf("error closing context %s: %v", ctxType, err)
		}
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.stopChan = nil
	cm.ctxCache = nil
	cm.config = nil
	cm.closed = true

	return nil
}
