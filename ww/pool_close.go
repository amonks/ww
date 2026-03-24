package ww

// Close closes the underlying database connection.
func (p *Pool) Close() error {
	if p == nil || p.close == nil {
		return nil
	}
	return p.close()
}
