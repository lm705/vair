package core

// The starred configurations, laid out where they can be seen.
//
// A star is kept in the settings file, keyed by the node body, so that it
// survives a rename, a list refresh and a move between tabs — which is the
// right behaviour and has one consequence nobody arranged: the entry outlives
// the configuration. A server that left the subscription a year ago is still in
// the file, still starred, and there is no row anywhere for it to appear on.
// The list only ever grew, and nothing in the program showed it.
//
// So: show it. Nothing expires on its own — a subscription that fails to load,
// or a tab the user has temporarily emptied, would otherwise take the stars of
// perfectly good configurations with it, and a star is a thing the user set by
// hand. What the program can do honestly is say which ones it cannot find right
// now and offer to drop them.

// FavoriteDTO is one starred configuration for the cleanup list.
type FavoriteDTO struct {
	// Body identifies it — the raw without its #name, exactly what is stored.
	Body string `json:"body"`
	// Raw is what to hand back when the user copies the row: the whole
	// configuration, name and all, when a tab still has it. A star that outlived
	// its configuration has only the body, which is the configuration minus a
	// label — still enough to paste back and connect with.
	Raw string `json:"raw"`
	// Name, Host and Proto describe it. The name comes from the row when the
	// configuration is still in a tab; a body carries no name of its own, so a
	// missing one is described by what it points at.
	Name  string `json:"name"`
	Host  string `json:"host"`
	Proto string `json:"proto"`
	// Tab is the title of a tab holding it, empty when it is nowhere.
	Tab string `json:"tab"`
	// Present is whether any tab has it right now. False is not proof the
	// configuration is gone — a source that failed to load looks the same.
	Present bool `json:"present"`
}

// Favorites returns every starred configuration, the ones still in a tab first
// (a body appears once however many tabs hold it).
func Favorites() []FavoriteDTO {
	settingsMu.RLock()
	favs := append([]string(nil), appSettings.Favorites...)
	settingsMu.RUnlock()
	if len(favs) == 0 {
		return []FavoriteDTO{}
	}

	// One walk over the tabs rather than one search per star: the pool can hold
	// a quarter of a million rows and the stars are a handful.
	want := make(map[string]int, len(favs))
	out := make([]FavoriteDTO, 0, len(favs))
	for _, f := range favs {
		body := nodeBody(f)
		if _, dup := want[body]; dup {
			continue
		}
		want[body] = len(out)
		out = append(out, FavoriteDTO{Body: body})
	}

	state.mu.RLock()
	titles := make(map[string]string, len(state.tabs))
	for _, t := range state.tabs {
		titles[t.ID] = t.Name
	}
	for tid, entries := range state.tabEntries {
		for _, e := range entries {
			es := e.snap()
			i, ok := want[nodeBody(es.Raw)]
			if !ok || out[i].Present {
				continue
			}
			out[i].Present = true
			out[i].Raw = es.Raw
			out[i].Name = es.Name
			out[i].Host = hostPort(es.Host, es.Port)
			out[i].Proto = es.Protocol
			out[i].Tab = titles[tid]
		}
	}
	state.mu.RUnlock()

	// Whatever no tab claimed is described from the stored body itself.
	for i := range out {
		if out[i].Present {
			continue
		}
		out[i].Raw = out[i].Body
		if n, err := parseNode(out[i].Body); err == nil {
			out[i].Host = hostPort(n.Host, n.Port)
			out[i].Proto = string(n.Kind)
		}
	}

	found := make([]FavoriteDTO, 0, len(out))
	lost := make([]FavoriteDTO, 0, len(out))
	for _, f := range out {
		if f.Present {
			found = append(found, f)
		} else {
			lost = append(lost, f)
		}
	}
	return append(found, lost...)
}

// RemoveFavorites unstars the given bodies and returns how many were removed.
// Passing the bodies rather than "remove everything missing" keeps the decision
// where the user made it: the list they were looking at, not whatever the tabs
// happen to hold by the time the click arrives.
func RemoveFavorites(bodies []string) int {
	if len(bodies) == 0 {
		return 0
	}
	drop := make(map[string]struct{}, len(bodies))
	for _, b := range bodies {
		drop[nodeBody(b)] = struct{}{}
	}
	settingsMu.Lock()
	kept := appSettings.Favorites[:0]
	removed := 0
	for _, f := range appSettings.Favorites {
		if _, gone := drop[nodeBody(f)]; gone {
			removed++
			continue
		}
		kept = append(kept, f)
	}
	appSettings.Favorites = append([]string(nil), kept...)
	settingsMu.Unlock()
	if removed == 0 {
		return 0
	}
	saveSettings()
	loadedSignal(TableTab()) // stars float to the top → re-sort what is on screen
	return removed
}
