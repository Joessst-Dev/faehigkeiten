package source

// Clone and Pull expose the git operations for sources that Fetch refuses,
// such as the plain-HTTP test server.
var (
	Clone = clone
	Pull  = pull
)
