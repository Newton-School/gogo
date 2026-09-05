package admin

func (s *Site) asset(path string) (body []byte, version, contentType string, legacy, ok bool) {
	for _, item := range []struct {
		extension            string
		body                 []byte
		version, contentType string
	}{
		{"css", s.css, s.cssVersion, "text/css; charset=utf-8"},
		{"js", s.js, s.jsVersion, "text/javascript; charset=utf-8"},
	} {
		stable := s.config.Prefix + "assets/admin." + item.extension
		fingerprinted := s.config.Prefix + "assets/admin." + item.version + "." + item.extension
		if path == stable || path == fingerprinted {
			return item.body, item.version, item.contentType, path == stable, true
		}
	}
	return nil, "", "", false, false
}
