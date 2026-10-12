package app

import "context"

// Counts reports how many repositories r loaded and directories it hashed.
func (r *Resolver) Counts() (loads, hashes int) { return r.loads.len(), r.hashes.len() }

func (c *memo[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Memo exposes the Resolver's cache to tests.
type Memo = memo[string]

func (c *memo[V]) Get(ctx context.Context, key string, fn func() (V, error)) (V, error) {
	return c.get(ctx, key, fn)
}
