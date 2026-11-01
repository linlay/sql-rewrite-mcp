package store

// Annotations are a presentation layer. Physical SQL identifiers never change.
// A nil comment follows the source; a pointer to "" explicitly clears it.
type Annotation struct {
	DisplayName string  `json:"display_name" yaml:"displayName"`
	Comment     *string `json:"comment" yaml:"comment"`
}

type TableAnnotation struct {
	Annotation `yaml:",inline"`
	Columns    map[string]Annotation `json:"columns" yaml:"columns"`
}

func (s *Store) applySchema(out *TableSchema) {
	if a, ok := s.Annotations[out.Name]; ok {
		out.DisplayName = a.DisplayName
		if a.Comment != nil {
			out.Comment = *a.Comment
		}
		for i := range out.Columns {
			c := &out.Columns[i]
			if v, ok := a.Columns[c.Name]; ok {
				c.DisplayName = v.DisplayName
				if v.Comment != nil {
					c.Comment = *v.Comment
				}
			}
		}
	}
}
