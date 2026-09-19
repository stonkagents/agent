package installenv

// Data folder resolution.
//
// The per-user data folder (identity in secrets.env, the SQLite databases,
// config, shared files and downloads) is .stonkagents, .stonkagents-dev or
// .stonkagents-stg under the user profile. Callers pass the path they are
// about to open through ResolveDataPath so a future rename can be handled in
// one place; today the path is returned unchanged.

// Logf receives one line per decision made while resolving paths. nil discards.
type Logf func(format string, args ...any)

// LegacyDataDirNames returns the earlier names of p.DataDirName. This release
// line carries none.
func (p Profile) LegacyDataDirNames() []string { return nil }

// ResolveDataPath returns the path to use for path, which may be the data
// folder itself or any file or folder inside it.
func ResolveDataPath(path string, logf Logf) (string, error) {
	_ = logf
	return path, nil
}

// MigrateLegacyDataDirs is called by the daemon and the controller at start.
// There is nothing to migrate in this release line.
func MigrateLegacyDataDirs(logf Logf) error {
	_ = logf
	return nil
}
