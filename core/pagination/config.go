package pagination

// Configuration returns the normalized configuration captured by New. The
// result is a value copy; changing it cannot change pagination behavior. Nil,
// zero-value and invalid paginators return ErrInvalid, not guessed defaults.
// Reading this metadata does not parse a query or invoke application code.
func (p *Paginator) Configuration() (Config, error) {
	if p == nil {
		return Config{}, ErrInvalid
	}
	config := p.config
	checked, err := New(config)
	if err != nil || checked.config != config {
		return Config{}, ErrInvalid
	}
	return config, nil
}
