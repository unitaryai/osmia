package jobbuilder

// MergePodAnnotations returns the annotations for an agent pod: the
// operator's configured extras overlaid with Osmia's own. Osmia's win on a
// clash, because they drive behaviour (for example the sandbox claim marker)
// and an operator setting must not be able to switch that off by accident.
//
// It always returns a fresh map, so a builder's configured annotations are
// never mutated by one Job and seen by the next. It returns nil when there is
// nothing to set, which keeps generated Job specs unchanged for deployments
// that configure no annotations.
func MergePodAnnotations(extra, own map[string]string) map[string]string {
	if len(extra) == 0 && len(own) == 0 {
		return nil
	}
	out := make(map[string]string, len(extra)+len(own))
	for k, v := range extra {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v
	}
	return out
}
