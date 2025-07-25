// This file is part of arduino-cli.
//
// Copyright 2020-2022 ARDUINO SA (http://www.arduino.cc/)
//
// This software is released under the GNU General Public License version 3,
// which covers the main part of arduino-cli.
// The terms of this license can be found at:
// https://www.gnu.org/licenses/gpl-3.0.en.html
//
// You can be released from the requirements of the above licenses by purchasing
// a commercial license. Buying such a license is mandatory if you want to
// modify or otherwise use the software for commercial activities involving the
// Arduino software without disclosing the source code of your own applications.
// To purchase a commercial license, send an email to license@arduino.cc.

package sketch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/arduino/arduino-cli/internal/arduino/utils"
	"github.com/arduino/arduino-cli/internal/i18n"
	rpc "github.com/arduino/arduino-cli/rpc/cc/arduino/cli/commands/v1"
	"github.com/arduino/go-paths-helper"
	"go.bug.st/f"
	semver "go.bug.st/relaxed-semver"
	"gopkg.in/yaml.v3"
)

// projectRaw is a support struct used only to unmarshal the yaml
type projectRaw struct {
	ProfilesRaw       yaml.Node         `yaml:"profiles"`
	DefaultProfile    string            `yaml:"default_profile"`
	DefaultFqbn       string            `yaml:"default_fqbn"`
	DefaultPort       string            `yaml:"default_port,omitempty"`
	DefaultPortConfig map[string]string `yaml:"default_port_config,omitempty"`
	DefaultProtocol   string            `yaml:"default_protocol,omitempty"`
	DefaultProgrammer string            `yaml:"default_programmer,omitempty"`
}

// Project represents the sketch project file
type Project struct {
	Profiles          []*Profile
	DefaultProfile    string
	DefaultFqbn       string
	DefaultPort       string
	DefaultPortConfig map[string]string
	DefaultProtocol   string
	DefaultProgrammer string
}

// AsYaml outputs the sketch project file as YAML
func (p *Project) AsYaml() string {
	res := "profiles:\n"

	for _, profile := range p.Profiles {
		res += fmt.Sprintf("  %s:\n", profile.Name)
		res += profile.AsYaml()
		res += "\n"
	}
	if p.DefaultProfile != "" {
		res += fmt.Sprintf("default_profile: %s\n", p.DefaultProfile)
	}
	if p.DefaultFqbn != "" {
		res += fmt.Sprintf("default_fqbn: %s\n", p.DefaultFqbn)
	}
	if p.DefaultPort != "" {
		res += fmt.Sprintf("default_port: %s\n", p.DefaultPort)
	}
	if len(p.DefaultPortConfig) > 0 {
		res += "default_port_config:\n"
		for k, v := range p.DefaultPortConfig {
			res += fmt.Sprintf("  %s: %s\n", k, v)
		}
	}
	if p.DefaultProtocol != "" {
		res += fmt.Sprintf("default_protocol: %s\n", p.DefaultProtocol)
	}
	if p.DefaultProgrammer != "" {
		res += fmt.Sprintf("default_programmer: %s\n", p.DefaultProgrammer)
	}
	return res
}

func (p *projectRaw) getProfiles() ([]*Profile, error) {
	profiles := []*Profile{}
	for i, node := range p.ProfilesRaw.Content {
		if node.Tag != "!!str" {
			continue // Node is a map, so it is read out at key.
		}

		var profile Profile
		profile.Name = node.Value
		if err := p.ProfilesRaw.Content[i+1].Decode(&profile); err != nil {
			return nil, err
		}
		profiles = append(profiles, &profile)
	}
	return profiles, nil
}

// UnmarshalYAML decodes a Profiles section from YAML source.
// Profile is a sketch profile, it contains a reference to all the resources
// needed to build and upload a sketch
type Profile struct {
	Name       string
	Notes      string                   `yaml:"notes"`
	FQBN       string                   `yaml:"fqbn"`
	Port       string                   `yaml:"port"`
	PortConfig map[string]string        `yaml:"port_config"`
	Protocol   string                   `yaml:"protocol"`
	Programmer string                   `yaml:"programmer"`
	Platforms  ProfileRequiredPlatforms `yaml:"platforms"`
	Libraries  ProfileRequiredLibraries `yaml:"libraries"`
}

// UsesSystemPlatform checks if this profile requires a system installed platform.
func (p *Profile) RequireSystemInstalledPlatform() bool {
	return p.Platforms[0].RequireSystemInstalledPlatform()
}

// ToRpc converts this Profile to an rpc.SketchProfile
func (p *Profile) ToRpc() *rpc.SketchProfile {
	var portConfig *rpc.MonitorPortConfiguration
	if len(p.PortConfig) > 0 {
		portConfig = &rpc.MonitorPortConfiguration{}
		for k, v := range p.PortConfig {
			portConfig.Settings = append(portConfig.Settings, &rpc.MonitorPortSetting{
				SettingId: k,
				Value:     v,
			})
		}
	}
	return &rpc.SketchProfile{
		Name:       p.Name,
		Fqbn:       p.FQBN,
		Programmer: p.Programmer,
		Port:       p.Port,
		PortConfig: portConfig,
		Protocol:   p.Protocol,
	}
}

// AsYaml outputs the profile as Yaml
func (p *Profile) AsYaml() string {
	res := ""
	if p.Notes != "" {
		res += fmt.Sprintf("    notes: %s\n", p.Notes)
	}
	res += fmt.Sprintf("    fqbn: %s\n", p.FQBN)
	if p.Programmer != "" {
		res += fmt.Sprintf("    programmer: %s\n", p.Programmer)
	}
	if p.Port != "" {
		res += fmt.Sprintf("    port: %s\n", p.Port)
	}
	if p.Protocol != "" {
		res += fmt.Sprintf("    protocol: %s\n", p.Protocol)
	}
	if len(p.PortConfig) > 0 {
		res += "     port_config:\n"
		for k, v := range p.PortConfig {
			res += fmt.Sprintf("       %s: %s\n", k, v)
		}
	}
	res += p.Platforms.AsYaml()
	res += p.Libraries.AsYaml()
	return res
}

// ProfileRequiredPlatforms is a list of ProfilePlatformReference (platforms
// required to build the sketch using this profile)
type ProfileRequiredPlatforms []*ProfilePlatformReference

// AsYaml outputs the required platforms as Yaml
func (p *ProfileRequiredPlatforms) AsYaml() string {
	res := "    platforms:\n"
	for _, platform := range *p {
		res += platform.AsYaml()
	}
	return res
}

func (p *ProfileRequiredPlatforms) UnmarshalYAML(unmarshal func(interface{}) error) error {
	_p := (*[]*ProfilePlatformReference)(p)
	if err := unmarshal(_p); err != nil {
		return err
	}
	requireSystemPlatform := (*_p)[0].RequireSystemInstalledPlatform()
	for _, platform := range *_p {
		if platform.RequireSystemInstalledPlatform() != requireSystemPlatform {
			return errors.New(i18n.Tr("all platforms in a profile must either require a specific version or not"))
		}
	}
	return nil
}

// ProfileRequiredLibraries is a list of ProfileLibraryReference (libraries
// required to build the sketch using this profile)
type ProfileRequiredLibraries []*ProfileLibraryReference

// AsYaml outputs the required libraries as Yaml
func (p *ProfileRequiredLibraries) AsYaml() string {
	if len(*p) == 0 {
		return ""
	}
	res := "    libraries:\n"
	for _, lib := range *p {
		res += lib.AsYaml()
	}
	return res
}

// ProfilePlatformReference is a reference to a platform
type ProfilePlatformReference struct {
	Packager         string
	Architecture     string
	Version          *semver.Version
	PlatformIndexURL *url.URL
}

// RequireSystemInstalledPlatform returns true if the platform reference
// does not specify a version, meaning it requires the system installed platform.
func (p *ProfilePlatformReference) RequireSystemInstalledPlatform() bool {
	return p.Version == nil
}

// InternalUniqueIdentifier returns the unique identifier for this object
func (p *ProfilePlatformReference) InternalUniqueIdentifier() string {
	id := p.String()
	h := sha256.Sum256([]byte(id))
	res := fmt.Sprintf("%s:%s@%s_%s", p.Packager, p.Architecture, p.Version, hex.EncodeToString(h[:])[:16])
	return utils.SanitizeName(res)
}

func (p *ProfilePlatformReference) String() string {
	res := fmt.Sprintf("%s:%s@%s", p.Packager, p.Architecture, p.Version)
	if p.PlatformIndexURL != nil {
		res += fmt.Sprintf(" (%s)", p.PlatformIndexURL)
	}
	return res
}

// AsYaml outputs the platform reference as Yaml
func (p *ProfilePlatformReference) AsYaml() string {
	res := ""
	if p.Version != nil {
		res += fmt.Sprintf("      - platform: %s:%s (%s)\n", p.Packager, p.Architecture, p.Version)
	} else {
		res += fmt.Sprintf("      - platform: %s:%s\n", p.Packager, p.Architecture)
	}
	if p.PlatformIndexURL != nil {
		res += fmt.Sprintf("        platform_index_url: %s\n", p.PlatformIndexURL)
	}
	return res
}

func parseNameAndVersion(in string) (string, string, bool) {
	{
		// Try to parse the input string in the format "VENDOR:ARCH (VERSION)"
		re := regexp.MustCompile(`^([a-zA-Z0-9.\-_ :]+) \((.+)\)$`)
		split := re.FindAllStringSubmatch(in, -1)
		if len(split) == 1 && len(split[0]) == 3 {
			return split[0][1], split[0][2], true
		}
	}

	{
		// Try to parse the input string in the format "VENDOR:ARCH"
		re := regexp.MustCompile(`^([a-zA-Z0-9.\-_ :]+)$`)
		split := re.FindAllStringSubmatch(in, -1)
		if len(split) == 1 && len(split[0]) == 2 {
			return split[0][1], "", true
		}
	}

	return "", "", false
}

// UnmarshalYAML decodes a ProfilePlatformReference from YAML source.
func (p *ProfilePlatformReference) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var data map[string]string
	if err := unmarshal(&data); err != nil {
		return err
	}
	if platformID, ok := data["platform"]; !ok {
		return errors.New(i18n.Tr("missing '%s' directive", "platform"))
	} else if platformID, platformVersion, ok := parseNameAndVersion(platformID); !ok {
		return errors.New(i18n.Tr("invalid '%s' directive", "platform"))
	} else {
		var version *semver.Version
		if platformVersion != "" {
			if v, err := semver.Parse(platformVersion); err != nil {
				return fmt.Errorf("%s: %w", i18n.Tr("error parsing version constraints"), err)
			} else {
				version = v
			}
		}

		if split := strings.SplitN(platformID, ":", 2); len(split) != 2 {
			return fmt.Errorf("%s: %s", i18n.Tr("invalid platform identifier"), platformID)
		} else {
			p.Packager = split[0]
			p.Architecture = split[1]
			p.Version = version
		}
	}

	if rawIndexURL, ok := data["platform_index_url"]; ok {
		indexURL, err := url.Parse(rawIndexURL)
		if err != nil {
			return fmt.Errorf("%s: %w", i18n.Tr("invalid platform index URL:"), err)
		}
		p.PlatformIndexURL = indexURL
	}
	return nil
}

// ProfileLibraryReference is a reference to a library
type ProfileLibraryReference struct {
	Library    string
	InstallDir *paths.Path
	Version    *semver.Version
}

// UnmarshalYAML decodes a ProfileLibraryReference from YAML source.
func (l *ProfileLibraryReference) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var dataMap map[string]any
	if err := unmarshal(&dataMap); err == nil {
		if installDir, ok := dataMap["dir"]; !ok {
			return errors.New(i18n.Tr("invalid library reference: %s", dataMap))
		} else if installDir, ok := installDir.(string); !ok {
			return fmt.Errorf("%s: %s", i18n.Tr("invalid library reference: %s"), dataMap)
		} else {
			l.InstallDir = paths.New(installDir)
			l.Library = l.InstallDir.Base()
			return nil
		}
	}

	var data string
	if err := unmarshal(&data); err != nil {
		return err
	}
	if libName, libVersion, ok := parseNameAndVersion(data); !ok {
		return fmt.Errorf("%s %s", i18n.Tr("invalid library directive:"), data)
	} else if v, err := semver.Parse(libVersion); err != nil {
		return fmt.Errorf("%s %w", i18n.Tr("invalid version:"), err)
	} else {
		l.Library = libName
		l.Version = v
	}
	return nil
}

// AsYaml outputs the required library as Yaml
func (l *ProfileLibraryReference) AsYaml() string {
	if l.InstallDir != nil {
		return fmt.Sprintf("      - dir: %s\n", l.InstallDir)
	}
	return fmt.Sprintf("      - %s (%s)\n", l.Library, l.Version)
}

func (l *ProfileLibraryReference) String() string {
	if l.InstallDir != nil {
		return fmt.Sprintf("%s@dir:%s", l.Library, l.InstallDir)
	}
	return fmt.Sprintf("%s@%s", l.Library, l.Version)
}

// InternalUniqueIdentifier returns the unique identifier for this object
func (l *ProfileLibraryReference) InternalUniqueIdentifier() string {
	f.Assert(l.InstallDir == nil,
		"InternalUniqueIdentifier should not be called for library references with an install directory")
	id := l.String()
	h := sha256.Sum256([]byte(id))
	res := fmt.Sprintf("%s_%s", id, hex.EncodeToString(h[:])[:16])
	return utils.SanitizeName(res)
}

// LoadProjectFile reads a sketch project file
func LoadProjectFile(file *paths.Path) (*Project, error) {
	data, err := file.ReadFile()
	if err != nil {
		return nil, err
	}
	raw := &projectRaw{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	profiles, err := raw.getProfiles()
	if err != nil {
		return nil, err
	}
	return &Project{
		Profiles:          profiles,
		DefaultProfile:    raw.DefaultProfile,
		DefaultFqbn:       raw.DefaultFqbn,
		DefaultPort:       raw.DefaultPort,
		DefaultPortConfig: raw.DefaultPortConfig,
		DefaultProtocol:   raw.DefaultProtocol,
		DefaultProgrammer: raw.DefaultProgrammer,
	}, nil
}
