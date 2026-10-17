package desktop

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo/ui"
)

type preferences struct {
	Version                          int
	Light, Compact                   bool
	Divider, ColumnSplit             float32
	SortColumn                       string
	SortDesc                         bool
	Columns, TypeColumns, TopColumns ui.TableLayout
	HiddenColumns                    map[string]bool
	RecentLocations                  []string
	Units, ScanMode, SearchScope     string
	View, AnalyticsTab, Metric       int
	Visualization                    int
	FollowSelection, ShowMetadata    bool
	AnalyticsCollapsed, MapCollapsed bool
}

func configDirectory() string {
	if directory := os.Getenv("STRATUM_CONFIG_DIR"); directory != "" {
		return directory
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, "Stratum")
}

func (a *App) rememberLocation(path string) {
	if position := slices.Index(a.recentLocations, path); position >= 0 {
		a.recentLocations = slices.Delete(a.recentLocations, position, position+1)
	}
	a.recentLocations = append([]string{path}, a.recentLocations...)
	if len(a.recentLocations) > 10 {
		a.recentLocations = a.recentLocations[:10]
	}
}

func validSplit(value, fallback float32) float32 {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 20 || value > 85 {
		return fallback
	}
	return value
}

func sanitizeColumns(layout ui.TableLayout) ui.TableLayout {
	for key, value := range layout.Widths {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 32 || value > 2000 {
			delete(layout.Widths, key)
		}
	}
	return layout
}

func (a *App) loadPreferences() {
	directory := configDirectory()
	if directory == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(directory, "preferences.json"))
	if err != nil {
		return
	}
	var saved preferences
	if json.Unmarshal(data, &saved) != nil {
		return
	}
	a.light, a.compact = saved.Light, saved.Compact
	a.divider, a.columnSplit = validSplit(saved.Divider, a.divider), validSplit(saved.ColumnSplit, a.columnSplit)
	if saved.Version == 1 && saved.Divider == 62 {
		a.divider = defaultDivider
	}
	if slices.Contains([]string{"Name", "% of Parent", "Size", "Allocated", "Files", "Folders", "Modified"}, saved.SortColumn) {
		a.sort = ui.SortOrder{Column: saved.SortColumn, Descending: saved.SortDesc}
	}
	if saved.Version < 1 || saved.Version > 2 {
		return
	}
	a.list.Columns, a.typeList.Columns, a.topList.Columns = sanitizeColumns(saved.Columns), sanitizeColumns(saved.TypeColumns), sanitizeColumns(saved.TopColumns)
	if saved.HiddenColumns != nil {
		a.hiddenColumns = saved.HiddenColumns
		delete(a.hiddenColumns, "Name")
	}
	if len(saved.RecentLocations) > 10 {
		saved.RecentLocations = saved.RecentLocations[:10]
	}
	a.recentLocations = saved.RecentLocations
	if saved.Units == "Decimal" {
		a.units = saved.Units
	}
	if saved.ScanMode == "Folders" {
		a.scanMode = saved.ScanMode
	}
	if saved.SearchScope == "Scan" {
		a.searchScope = "Scan"
	}
	if saved.View >= 0 && saved.View <= 2 {
		a.tab = saved.View
	}
	if saved.AnalyticsTab >= 0 && saved.AnalyticsTab <= 3 {
		a.typeTab = saved.AnalyticsTab
	}
	if saved.Visualization >= 0 && saved.Visualization <= 3 {
		a.mapTab = saved.Visualization
	}
	if saved.Metric >= 0 && saved.Metric <= 1 {
		a.metric = saved.Metric
	}
	a.followSelection, a.showMetadata = saved.FollowSelection, saved.ShowMetadata
	a.analyticsCollapsed, a.mapCollapsed = saved.AnalyticsCollapsed, saved.MapCollapsed
}

func (a *App) savePreferences() {
	directory := configDirectory()
	if directory == "" {
		return
	}
	saved := preferences{Version: 2, Light: a.light, Compact: a.compact, Divider: a.divider, ColumnSplit: a.columnSplit, SortColumn: a.sort.Column, SortDesc: a.sort.Descending, Columns: a.list.Columns, TypeColumns: a.typeList.Columns, TopColumns: a.topList.Columns, HiddenColumns: a.hiddenColumns, RecentLocations: a.recentLocations, Units: a.units, ScanMode: a.scanMode, SearchScope: a.searchScope, View: a.tab, AnalyticsTab: a.typeTab, Metric: a.metric, FollowSelection: a.followSelection, ShowMetadata: a.showMetadata, AnalyticsCollapsed: a.analyticsCollapsed, MapCollapsed: a.mapCollapsed}
	err := os.MkdirAll(directory, 0755)
	saved.Visualization = a.mapTab
	if err == nil {
		err = writePreferences(filepath.Join(directory, "preferences.json"), saved)
	}
	if err != nil {
		a.notice = "Could not save preferences: " + err.Error()
	}
}

func writePreferences(path string, saved preferences) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".preferences-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(saved)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (a *App) formatBytes(value uint64) string {
	if a.units != "Decimal" {
		return Bytes(value)
	}
	if value < 1000 {
		return fmt.Sprintf("%d B", value)
	}
	amount := float64(value) / 1000
	unit := 0
	units := []string{"kB", "MB", "GB", "TB", "PB", "EB"}
	for amount >= 1000 && unit < len(units)-1 {
		amount /= 1000
		unit++
	}
	return fmt.Sprintf("%.2f %s", amount, units[unit])
}
