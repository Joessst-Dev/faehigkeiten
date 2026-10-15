package source

// Clone and Pull expose the git operations for sources that Fetch refuses,
// such as the plain-HTTP test server.
var (
	Clone = clone
	Pull  = pull
)

// CachePath exposes where sources and commit trees are cached.
func (m *Manager) CachePath(src, ref string) string { return m.cachePath(src, ref) }
