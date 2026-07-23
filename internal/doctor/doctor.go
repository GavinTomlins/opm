package doctor

import (
	"fmt"
	"os"

	"github.com/tbcrawford/opm/internal/preset"
	"github.com/tbcrawford/opm/internal/store"
	"github.com/tbcrawford/opm/internal/symlink"
)

type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFail
)

type Row struct {
	Status      Status
	Message     string
	ProfileName string
}

type Section struct {
	Label string
	Rows  []Row
}

type Report struct {
	Sections     []Section
	WarningCount int
	FailureCount int
	HasFailures  bool
}

func Run(s *store.Store) Report {
	report := Report{}
	symlinkSection := Section{Label: "Symlink"}

	managed, err := s.IsOpmManaged()
	if err != nil {
		symlinkSection.Rows = append(symlinkSection.Rows, Row{
			Status:  StatusFail,
			Message: fmt.Sprintf("~/.config/opencode: %v", err),
		})
		report.Sections = append(report.Sections, symlinkSection)
		report.FailureCount = 1
		report.HasFailures = true
		return report
	}
	if !managed {
		symlinkSection.Rows = append(symlinkSection.Rows, Row{
			Status:  StatusFail,
			Message: "~/.config/opencode is not an opm-managed symlink — run 'opm init'",
		})
		report.Sections = append(report.Sections, symlinkSection)
		report.FailureCount = 1
		report.HasFailures = true
		return report
	}

	st, err := symlink.Inspect(s.OpencodeDir())
	if err != nil {
		symlinkSection.Rows = append(symlinkSection.Rows, Row{
			Status:  StatusFail,
			Message: fmt.Sprintf("inspect ~/.config/opencode: %v", err),
		})
		report.FailureCount++
	} else if st.Dangling {
		symlinkSection.Rows = append(symlinkSection.Rows, Row{
			Status:  StatusFail,
			Message: fmt.Sprintf("~/.config/opencode → %q (profile directory missing)", st.Target),
		})
		report.FailureCount++
	} else {
		activeName, _ := s.ActiveProfile()
		symlinkSection.Rows = append(symlinkSection.Rows, Row{
			Status:      StatusOK,
			Message:     "~/.config/opencode → %s",
			ProfileName: activeName,
		})
	}
	report.Sections = append(report.Sections, symlinkSection)

	profilesSection := Section{Label: "Profiles"}
	profiles, err := s.ListProfiles()
	if err != nil {
		profilesSection.Rows = append(profilesSection.Rows, Row{
			Status:  StatusFail,
			Message: fmt.Sprintf("list profiles: %v", err),
		})
		report.FailureCount++
	} else {
		for _, p := range profiles {
			if p.Dangling {
				profilesSection.Rows = append(profilesSection.Rows, Row{
					Status:      StatusFail,
					Message:     "%s — directory missing",
					ProfileName: p.Name,
				})
				report.FailureCount++
				continue
			}
			fi, statErr := os.Lstat(p.Path)
			if statErr != nil || !fi.IsDir() {
				profilesSection.Rows = append(profilesSection.Rows, Row{
					Status:      StatusFail,
					Message:     "%s — not a valid directory (" + p.Path + ")",
					ProfileName: p.Name,
				})
				report.FailureCount++
			} else {
				profilesSection.Rows = append(profilesSection.Rows, Row{
					Status:      StatusOK,
					Message:     "%s",
					ProfileName: p.Name,
				})
			}
		}
	}
	report.Sections = append(report.Sections, profilesSection)

	current, curErr := s.GetCurrent()
	active, actErr := s.ActiveProfile()
	if curErr == nil && actErr == nil && current != "" && active != "" && current != active {
		report.Sections = append(report.Sections, Section{
			Label: "Consistency",
			Rows: []Row{{
				Status:  StatusWarn,
				Message: fmt.Sprintf("current file says %q but active symlink points to %q", current, active),
			}},
		})
		report.WarningCount++
	}

	report.HasFailures = report.FailureCount > 0
	return report
}

// RunPresets appends preset health checks to a report: each stored preset
// must parse, resolve its extends chain, and reference only model refs the
// profile's opencode.json can account for. Also warns when shadowed
// oh-my-openagent config candidates exist.
func RunPresets(report *Report, pm *preset.Manager) {
	infos, err := pm.List()
	if err != nil {
		report.Sections = append(report.Sections, Section{
			Label: "Presets",
			Rows:  []Row{{Status: StatusFail, Message: fmt.Sprintf("list presets: %v", err)}},
		})
		report.FailureCount++
		report.HasFailures = true
		return
	}

	section := Section{Label: "Presets"}

	if lf, err := pm.LiveFile(); err == nil && len(lf.Others) > 0 {
		for _, other := range lf.Others {
			section.Rows = append(section.Rows, Row{
				Status:  StatusWarn,
				Message: fmt.Sprintf("%s is shadowed by %s and never loaded", other, lf.Path),
			})
			report.WarningCount++
		}
	}

	for _, info := range infos {
		if info.Err != nil {
			section.Rows = append(section.Rows, Row{
				Status:      StatusFail,
				Message:     "%s — " + info.Err.Error(),
				ProfileName: info.Name,
			})
			report.FailureCount++
			continue
		}
		resolved, err := pm.Resolve(info.Name)
		if err != nil {
			section.Rows = append(section.Rows, Row{
				Status:      StatusFail,
				Message:     "%s — " + err.Error(),
				ProfileName: info.Name,
			})
			report.FailureCount++
			continue
		}
		issues, err := pm.ValidateRefs(resolved)
		if err != nil {
			section.Rows = append(section.Rows, Row{
				Status:      StatusFail,
				Message:     "%s — " + err.Error(),
				ProfileName: info.Name,
			})
			report.FailureCount++
			continue
		}

		fails, warns := 0, 0
		for _, issue := range issues {
			if issue.Severity == preset.SeverityFail {
				fails++
			} else {
				warns++
			}
		}
		switch {
		case fails > 0:
			section.Rows = append(section.Rows, Row{
				Status:      StatusFail,
				Message:     fmt.Sprintf("%%s — %d invalid model ref(s); run 'opm preset diff %s'", fails, info.Name),
				ProfileName: info.Name,
			})
			report.FailureCount++
		case warns > 0:
			section.Rows = append(section.Rows, Row{
				Status:      StatusWarn,
				Message:     fmt.Sprintf("%%s — %d model ref warning(s)", warns),
				ProfileName: info.Name,
			})
			report.WarningCount++
		default:
			section.Rows = append(section.Rows, Row{
				Status:      StatusOK,
				Message:     "%s",
				ProfileName: info.Name,
			})
		}
	}

	if len(section.Rows) > 0 {
		report.Sections = append(report.Sections, section)
	}
	report.HasFailures = report.FailureCount > 0
}
