package tui

// helperProgress identifies the progress owner without relying on tab kind,
// visibility, or the order in which other confirmation tabs were opened.
func helperProgress(screen Screen) *InstallProgressScreen {
	switch s := screen.(type) {
	case *SyncthingInstallScreen:
		return s.progress
	case *P2PUpgradeScreen:
		return s.progress
	}
	return nil
}

func (m Model) helperTabBusy(tab openTab) bool {
	busy := func(screen Screen) bool {
		progress := helperProgress(screen)
		return progress != nil && !progress.done
	}
	if busy(tab.Screen) {
		return true
	}
	for _, child := range m.tabs {
		if child.Section == tab.Section && child.Parent == tab.Kind && busy(child.Screen) {
			return true
		}
	}
	return false
}
