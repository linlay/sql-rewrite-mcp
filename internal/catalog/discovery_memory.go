package catalog

import (
	"encoding/json"
	"time"
)

// Each Catalog belongs to one server process. At most one candidate per source
// is retained until approval, replacement, baseline/connection change or restart.
// No expiry timer interrupts the ordinary inspect/review flow.
type discoveryRuntime struct {
	checked, attempted time.Time
	error              string
	pending            *PendingDiscovery
	source             string
	reports            []SchemaReport
}

func (c *Catalog) discoveryRuntimeLocked(id string) *discoveryRuntime {
	if c.discoveryState == nil {
		c.discoveryState = map[string]*discoveryRuntime{}
	}
	if c.discoveryState[id] == nil {
		c.discoveryState[id] = &discoveryRuntime{}
	}
	return c.discoveryState[id]
}

func discoverySource(d *Database) string {
	raw, _ := json.Marshal([]any{d.Driver, d.Path, d.Connection})
	return string(raw)
}

func (c *Catalog) recordDiscoveryCheck(id string, checked time.Time, scanErr error) {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	s := c.discoveryRuntimeLocked(id)
	if checked.After(s.checked) {
		s.checked = checked
	}
	s.attempted = time.Now().UTC()
	s.error = ""
	if scanErr != nil {
		s.error = scanErr.Error()
	}
}

func (c *Catalog) recordDiscoveryReport(id string, report SchemaReport) {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	s := c.discoveryRuntimeLocked(id)
	s.reports = append(s.reports, report)
	if len(s.reports) > 20 {
		s.reports = s.reports[len(s.reports)-20:]
	}
}

func (c *Catalog) overlayDiscovery(items []Database) {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	for i := range items {
		d := &items[i]
		if d.Discovery == nil {
			continue
		}
		// Disk data from an older deployment never revives pending candidates.
		d.Discovery.Pending = nil
		d.Discovery.CheckedAt = time.Time{}
		d.Discovery.AttemptedAt = time.Time{}
		d.Discovery.Error = ""
		d.Discovery.Reports = nil
		s := c.discoveryState[d.ID]
		if s == nil {
			continue
		}
		d.Discovery.CheckedAt, d.Discovery.AttemptedAt, d.Discovery.Error = s.checked, s.attempted, s.error
		// Copy before returning: readers must never mutate process-owned state.
		raw, _ := json.Marshal(s.reports)
		_ = json.Unmarshal(raw, &d.Discovery.Reports)
		if p := s.pending; p != nil {
			if p.BaseVersion != d.Discovery.Version || s.source != discoverySource(d) {
				s.pending = nil
				continue
			}
			raw, _ := json.Marshal(p)
			_ = json.Unmarshal(raw, &d.Discovery.Pending)
			d.Discovery.Pending.Changes = schemaDiff(d.Discovery.Tables, p.Tables, d.Discovery.Annotations)
		}
	}
}
