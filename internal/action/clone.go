package action

// StaticSQL returns the SQL templates the Action compiler can determine.
// It never runs JavaScript or SQL and does not establish business compatibility.
func (d Definition) StaticSQL(libraries ...Library) ([]string, error) {
	if err := d.Validate(libraries...); err != nil {
		return nil, err
	}
	_, sites, err := compileWithLibraries(d.Script, libraries, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.sql)
	}
	return out, nil
}
