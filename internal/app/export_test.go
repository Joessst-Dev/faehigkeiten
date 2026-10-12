package app

// Counts reports how many repositories r loaded and directories it hashed.
func (r *Resolver) Counts() (loads, hashes int) { return r.loads.len(), r.hashes.len() }
