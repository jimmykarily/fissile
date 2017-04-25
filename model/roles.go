package model

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hpcloud/fissile/validation"

	"gopkg.in/yaml.v2"
)

// RoleType is the type of the role; see the constants below
type RoleType string

// These are the types of roles available
const (
	RoleTypeBoshTask = RoleType("bosh-task") // A role that is a BOSH task
	RoleTypeBosh     = RoleType("bosh")      // A role that is a BOSH job
	RoleTypeDocker   = RoleType("docker")    // A role that is a raw Docker image
)

// FlightStage describes when a role should be executed
type FlightStage string

// These are the flight stages available
const (
	FlightStagePreFlight  = FlightStage("pre-flight")  // A role that runs before the main jobs start
	FlightStageFlight     = FlightStage("flight")      // A role that is a main job
	FlightStagePostFlight = FlightStage("post-flight") // A role that runs after the main jobs are up
	FlightStageManual     = FlightStage("manual")      // A role that only runs via user intervention
)

// RoleManifest represents a collection of roles
type RoleManifest struct {
	Roles         Roles          `yaml:"roles"`
	Configuration *Configuration `yaml:"configuration"`

	manifestFilePath string
	rolesByName      map[string]*Role
}

// Role represents a collection of jobs that are colocated on a container
type Role struct {
	Name              string         `yaml:"name"`
	Jobs              Jobs           `yaml:"_,omitempty"`
	EnvironScripts    []string       `yaml:"environment_scripts"`
	Scripts           []string       `yaml:"scripts"`
	PostConfigScripts []string       `yaml:"post_config_scripts"`
	Type              RoleType       `yaml:"type,omitempty"`
	JobNameList       []*roleJob     `yaml:"jobs"`
	Configuration     *Configuration `yaml:"configuration"`
	Run               *RoleRun       `yaml:"run"`
	Tags              []string       `yaml:"tags"`

	rolesManifest *RoleManifest
}

// RoleRun describes how a role should behave at runtime
type RoleRun struct {
	Scaling           *RoleRunScaling       `yaml:"scaling"`
	Capabilities      []string              `yaml:"capabilities"`
	PersistentVolumes []*RoleRunVolume      `yaml:"persistent-volumes"`
	SharedVolumes     []*RoleRunVolume      `yaml:"shared-volumes"`
	Memory            int                   `yaml:"memory"`
	VirtualCPUs       int                   `yaml:"virtual-cpus"`
	ExposedPorts      []*RoleRunExposedPort `yaml:"exposed-ports"`
	FlightStage       FlightStage           `yaml:"flight-stage"`
	HealthCheck       *HealthCheck          `yaml:"healthcheck,omitempty"`
	Environment       []string              `yaml:"env"`
}

// RoleRunScaling describes how a role should scale out at runtime
type RoleRunScaling struct {
	Min int32 `yaml:"min"`
	Max int32 `yaml:"max"`
}

// RoleRunVolume describes a volume to be attached at runtime
type RoleRunVolume struct {
	Path string `yaml:"path"`
	Tag  string `yaml:"tag"`
	Size int    `yaml:"size"`
}

// RoleRunExposedPort describes a port to be available to other roles, or the outside world
type RoleRunExposedPort struct {
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	External string `yaml:"external"`
	Internal string `yaml:"internal"`
	Public   bool   `yaml:"public"`
}

// HealthCheck describes a non-standard health check endpoint
type HealthCheck struct {
	URL     string            `yaml:"url"`     // URL for a HTTP GET to return 200~399. Cannot be used with other checks.
	Headers map[string]string `yaml:"headers"` // Custom headers; only used for URL.
	Command []string          `yaml:"command"` // Custom command. Cannot be used with other checks.
	Port    int32             `yaml:"port"`    // Port for a TCP probe. Cannot be used with other checks.
}

// Roles is an array of Role*
type Roles []*Role

// Configuration contains information about how to configure the
// resulting images
type Configuration struct {
	Templates map[string]string          `yaml:"templates"`
	Variables ConfigurationVariableSlice `yaml:"variables"`
}

// ConfigurationVariable is a configuration to be exposed to the IaaS
type ConfigurationVariable struct {
	Name        string                          `yaml:"name"`
	Default     interface{}                     `yaml:"default"`
	Description string                          `yaml:"description"`
	Generator   *ConfigurationVariableGenerator `yaml:"generator"`
	Private     bool                            `yaml:"private,omitempty"`
}

// CVMap is a map from variable name to ConfigurationVariable, for
// various places which require quick access/search/existence check.
type CVMap map[string]*ConfigurationVariable

// ConfigurationVariableSlice is a sortable slice of ConfigurationVariables
type ConfigurationVariableSlice []*ConfigurationVariable

// Len is the number of ConfigurationVariables in the slice
func (confVars ConfigurationVariableSlice) Len() int {
	return len(confVars)
}

// Less reports whether config variable at index i sort before the one at index j
func (confVars ConfigurationVariableSlice) Less(i, j int) bool {
	return strings.Compare(confVars[i].Name, confVars[j].Name) < 0
}

// Swap exchanges configuration variables at index i and index j
func (confVars ConfigurationVariableSlice) Swap(i, j int) {
	confVars[i], confVars[j] = confVars[j], confVars[i]
}

// ConfigurationVariableGenerator describes how to automatically generate values
// for a configuration variable
type ConfigurationVariableGenerator struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	ValueType string `yaml:"value_type"`
}

type roleJob struct {
	Name        string `yaml:"name"`
	ReleaseName string `yaml:"release_name"`
}

// Len is the number of roles in the slice
func (roles Roles) Len() int {
	return len(roles)
}

// Less reports whether role at index i sort before role at index j
func (roles Roles) Less(i, j int) bool {
	return strings.Compare(roles[i].Name, roles[j].Name) < 0
}

// Swap exchanges roles at index i and index j
func (roles Roles) Swap(i, j int) {
	roles[i], roles[j] = roles[j], roles[i]
}

// LoadRoleManifest loads a yaml manifest that details how jobs get grouped into roles
func LoadRoleManifest(manifestFilePath string, releases []*Release) (*RoleManifest, error) {
	manifestContents, err := ioutil.ReadFile(manifestFilePath)
	if err != nil {
		return nil, err
	}

	mappedReleases := map[string]*Release{}

	for _, release := range releases {
		_, ok := mappedReleases[release.Name]

		if ok {
			return nil, fmt.Errorf("Error - release %s has been loaded more than once", release.Name)
		}

		mappedReleases[release.Name] = release
	}

	rolesManifest := RoleManifest{}
	rolesManifest.manifestFilePath = manifestFilePath
	if err := yaml.Unmarshal(manifestContents, &rolesManifest); err != nil {
		return nil, err
	}

	if rolesManifest.Configuration == nil {
		rolesManifest.Configuration = &Configuration{}
	}
	if rolesManifest.Configuration.Templates == nil {
		rolesManifest.Configuration.Templates = map[string]string{}
	}

	// See also 'GetVariablesForRole' (mustache.go).
	declaredConfigs := MakeMapOfVariables(&rolesManifest)

	allErrs := validation.ErrorList{}

	for i := len(rolesManifest.Roles) - 1; i >= 0; i-- {
		role := rolesManifest.Roles[i]

		// Remove all roles that are not of the "bosh" or "bosh-task" type
		// Default type is considered to be "bosh".
		switch role.Type {
		case "":
			role.Type = RoleTypeBosh
		case RoleTypeBosh, RoleTypeBoshTask:
			continue
		case RoleTypeDocker:
			rolesManifest.Roles = append(rolesManifest.Roles[:i], rolesManifest.Roles[i+1:]...)
		default:
			allErrs = append(allErrs, validation.Invalid(
				fmt.Sprintf("roles[%s].type", role.Name),
				role.Type, "Excpected one of bosh, bosh-task, or docker"))
		}

		allErrs = append(allErrs, validateRoleRun(role, &rolesManifest, declaredConfigs)...)
	}

	rolesManifest.rolesByName = make(map[string]*Role, len(rolesManifest.Roles))

	for _, role := range rolesManifest.Roles {
		role.rolesManifest = &rolesManifest
		role.Jobs = make(Jobs, 0, len(role.JobNameList))

		for _, roleJob := range role.JobNameList {
			release, ok := mappedReleases[roleJob.ReleaseName]

			if !ok {
				allErrs = append(allErrs, validation.Invalid(
					fmt.Sprintf("roles[%s].jobs[%s]", role.Name, roleJob.Name),
					roleJob.ReleaseName,
					"Referenced release is not loaded"))
				continue
			}

			job, err := release.LookupJob(roleJob.Name)
			if err != nil {
				allErrs = append(allErrs, validation.Invalid(
					fmt.Sprintf("roles[%s].jobs[%s]", role.Name, roleJob.Name),
					roleJob.ReleaseName, err.Error()))
				continue
			}

			role.Jobs = append(role.Jobs, job)
		}

		role.calculateRoleConfigurationTemplates()
		rolesManifest.rolesByName[role.Name] = role
	}

	allErrs = append(allErrs, validateVariableSorting(rolesManifest.Configuration.Variables)...)
	allErrs = append(allErrs, validateVariableUsage(&rolesManifest)...)
	allErrs = append(allErrs, validateTemplateUsage(&rolesManifest)...)
	allErrs = append(allErrs, validateNonTemplates(&rolesManifest)...)

	if len(allErrs) != 0 {
		return nil, fmt.Errorf(allErrs.Errors())
	}

	return &rolesManifest, nil
}

// GetRoleManifestDevPackageVersion gets the aggregate signature of all the packages
func (m *RoleManifest) GetRoleManifestDevPackageVersion(roles Roles, extra string) (string, error) {
	// Make sure our roles are sorted, to have consistent output
	roles = append(Roles{}, roles...)
	sort.Sort(roles)

	hasher := sha1.New()
	hasher.Write([]byte(extra))

	for _, role := range roles {
		version, err := role.GetRoleDevVersion()
		if err != nil {
			return "", err
		}
		hasher.Write([]byte(version))
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// LookupRole will find the given role in the role manifest
func (m *RoleManifest) LookupRole(roleName string) *Role {
	return m.rolesByName[roleName]
}

// SelectRoles will find only the given roles in the role manifest
func (m *RoleManifest) SelectRoles(roleNames []string) (Roles, error) {
	if len(roleNames) == 0 {
		// No role names specified, assume all roles
		return m.Roles, nil
	}

	var results Roles
	var missingRoles []string

	for _, roleName := range roleNames {
		if role, ok := m.rolesByName[roleName]; ok {
			results = append(results, role)
		} else {
			missingRoles = append(missingRoles, roleName)
		}
	}
	if len(missingRoles) > 0 {
		return nil, fmt.Errorf("Some roles are unknown: %v", missingRoles)
	}

	return results, nil
}

// GetScriptPaths returns the paths to the startup / post configgin scripts for a role
func (r *Role) GetScriptPaths() map[string]string {
	result := map[string]string{}

	for _, scriptList := range [][]string{r.EnvironScripts, r.Scripts, r.PostConfigScripts} {
		for _, script := range scriptList {
			if filepath.IsAbs(script) {
				// Absolute paths _inside_ the container; there is nothing to copy
				continue
			}
			result[script] = filepath.Join(filepath.Dir(r.rolesManifest.manifestFilePath), script)
		}
	}

	return result

}

// GetScriptSignatures returns the SHA1 of all of the script file names and contents
func (r *Role) GetScriptSignatures() (string, error) {
	hasher := sha1.New()

	i := 0
	paths := r.GetScriptPaths()
	scripts := make([]string, len(paths))

	for _, f := range paths {
		scripts[i] = f
		i++
	}

	sort.Strings(scripts)

	for _, filename := range scripts {
		hasher.Write([]byte(filename))

		f, err := os.Open(filename)
		if err != nil {
			return "", err
		}

		if _, err := io.Copy(hasher, f); err != nil {
			return "", err
		}

		f.Close()
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// GetTemplateSignatures returns the SHA1 of all of the templates and contents
func (r *Role) GetTemplateSignatures() (string, error) {
	hasher := sha1.New()

	i := 0
	templates := make([]string, len(r.Configuration.Templates))

	for k, v := range r.Configuration.Templates {
		templates[i] = fmt.Sprintf("%s: %s", k, v)
		i++
	}

	sort.Strings(templates)

	for _, template := range templates {
		hasher.Write([]byte(template))
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// GetRoleDevVersion gets the aggregate signature of all jobs and packages
func (r *Role) GetRoleDevVersion() (string, error) {
	roleSignature := ""
	var packages Packages

	// Jobs are *not* sorted because they are an array and the order may be
	// significant, in particular for bosh-task roles.
	for _, job := range r.Jobs {
		roleSignature = fmt.Sprintf("%s\n%s", roleSignature, job.SHA1)
		packages = append(packages, job.Packages...)
	}

	sort.Sort(packages)
	for _, pkg := range packages {
		roleSignature = fmt.Sprintf("%s\n%s", roleSignature, pkg.SHA1)
	}

	// Collect signatures for various script sections
	sig, err := r.GetScriptSignatures()
	if err != nil {
		return "", err
	}
	roleSignature = fmt.Sprintf("%s\n%s", roleSignature, sig)

	// If there are templates, generate signature for them
	if r.Configuration != nil && r.Configuration.Templates != nil {
		sig, err = r.GetTemplateSignatures()
		if err != nil {
			return "", err
		}
		roleSignature = fmt.Sprintf("%s\n%s", roleSignature, sig)
	}

	hasher := sha1.New()
	hasher.Write([]byte(roleSignature))
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// HasTag returns true if the role has a specific tag
func (r *Role) HasTag(tag string) bool {
	for _, t := range r.Tags {
		if t == tag {
			return true
		}
	}

	return false
}

func (r *Role) calculateRoleConfigurationTemplates() {
	if r.Configuration == nil {
		r.Configuration = &Configuration{}
	}
	if r.Configuration.Templates == nil {
		r.Configuration.Templates = map[string]string{}
	}

	roleConfigs := map[string]string{}
	for k, v := range r.rolesManifest.Configuration.Templates {
		roleConfigs[k] = v
	}

	for k, v := range r.Configuration.Templates {
		roleConfigs[k] = v
	}

	r.Configuration.Templates = roleConfigs
}

// validateVariableSorting tests whether the parameters are properly sorted or not.
// It reports all variables which are out of order.
func validateVariableSorting(variables ConfigurationVariableSlice) validation.ErrorList {
	allErrs := validation.ErrorList{}

	previousName := ""
	for _, cv := range variables {
		if cv.Name < previousName {
			allErrs = append(allErrs, validation.Invalid("configuration.variables",
				previousName,
				fmt.Sprintf("Does not sort before '%s'", cv.Name)))
		}
		previousName = cv.Name
	}

	return allErrs
}

// validateVariableUsage tests whether all parameters are used in a
// template or not.  It reports all variables which are not used by at
// least one template.  An exception are the variables marked with
// `private: true`. These are not reported.  Use this to declare
// variables used only in the various scripts and not in templates.
func validateVariableUsage(roleManifest *RoleManifest) validation.ErrorList {
	allErrs := validation.ErrorList{}

	// See also 'GetVariablesForRole' (mustache.go).

	unusedConfigs := MakeMapOfVariables(roleManifest)
	if len(unusedConfigs) == 0 {
		return allErrs
	}

	// Iterate over all roles, jobs, templates, extract the used
	// variables. Remove each found from the set of unused
	// configs.

	for _, role := range roleManifest.Roles {
		for _, job := range role.Jobs {
			for _, property := range job.Properties {
				propertyName := fmt.Sprintf("properties.%s", property.Name)

				if template, ok := role.Configuration.Templates[propertyName]; ok {
					varsInTemplate, err := parseTemplate(template)
					if err != nil {
						// Ignore bad template, cannot have sensible
						// variable references
						continue
					}
					for _, envVar := range varsInTemplate {
						if _, ok := unusedConfigs[envVar]; ok {
							delete(unusedConfigs, envVar)
						}
						if len(unusedConfigs) == 0 {
							// Everything got used, stop now.
							return allErrs
						}
					}
				}
			}
		}
	}

	// Iterate over the global templates, extract the used
	// variables. Remove each found from the set of unused
	// configs.

	// Note, we have to ignore bad templates (no sensible variable
	// references) and continue to check everything else.

	for _, template := range roleManifest.Configuration.Templates {
		varsInTemplate, err := parseTemplate(template)
		if err != nil {
			continue
		}
		for _, envVar := range varsInTemplate {
			if _, ok := unusedConfigs[envVar]; ok {
				delete(unusedConfigs, envVar)
			}
			if len(unusedConfigs) == 0 {
				// Everything got used, stop now.
				return allErrs
			}
		}
	}

	// We have only the unused variables left in the set. Report
	// those which are not private.

	for cv, cvar := range unusedConfigs {
		if cvar.Private {
			continue
		}

		allErrs = append(allErrs, validation.NotFound("configuration.variables",
			fmt.Sprintf("No templates using '%s'", cv)))
	}

	return allErrs
}

// validateTemplateUsage tests whether all templates use only declared variables or not.
// It reports all undeclared variables.
func validateTemplateUsage(roleManifest *RoleManifest) validation.ErrorList {
	allErrs := validation.ErrorList{}

	// See also 'GetVariablesForRole' (mustache.go), and LoadManifest (caller, this file)
	declaredConfigs := MakeMapOfVariables(roleManifest)

	// Fissile provides some configuration variables by itself, see
	// --> scripts/dockerfiles/run.sh, add them to prevent them from being reported as errors.
	// The code here has to match the list of variables there.
	declaredConfigs["IP_ADDRESS"] = &ConfigurationVariable{Name: "IP_ADDRESS"}
	declaredConfigs["DNS_RECORD_NAME"] = &ConfigurationVariable{Name: "DNS_RECORD_NAME"}

	// Iterate over all roles, jobs, templates, extract the used
	// variables. Report all without a declaration.

	for _, role := range roleManifest.Roles {

		// Note, we cannot use GetVariablesForRole here
		// because it will abort on bad templates. Here we
		// have to ignore them (no sensible variable
		// references) and continue to check everything else.

		for _, job := range role.Jobs {
			for _, property := range job.Properties {
				propertyName := fmt.Sprintf("properties.%s", property.Name)

				if template, ok := role.Configuration.Templates[propertyName]; ok {
					varsInTemplate, err := parseTemplate(template)
					if err != nil {
						continue
					}
					for _, envVar := range varsInTemplate {
						if _, ok := declaredConfigs[envVar]; ok {
							continue
						}

						allErrs = append(allErrs, validation.NotFound("configuration.variables",
							fmt.Sprintf("No declaration of '%s'", envVar)))

						// Add a placeholder so that this variable is not reported again.
						// One report is good enough.
						declaredConfigs[envVar] = nil
					}
				}
			}
		}
	}

	// Iterate over the global templates, extract the used
	// variables. Report all without a declaration.

	for _, template := range roleManifest.Configuration.Templates {
		varsInTemplate, err := parseTemplate(template)
		if err != nil {
			// Ignore bad template, cannot have sensible
			// variable references
			continue
		}
		for _, envVar := range varsInTemplate {
			if _, ok := declaredConfigs[envVar]; ok {
				continue
			}

			allErrs = append(allErrs, validation.NotFound("configuration.templates",
				fmt.Sprintf("No variable declaration of '%s'", envVar)))

			// Add a placeholder so that this variable is
			// not reported again.  One report is good
			// enough.
			declaredConfigs[envVar] = nil
		}
	}

	return allErrs
}

// validateRoleRun tests whether required fields in the RoleRun are
// set. Note, some of the fields have type-dependent checks. Some
// issues are fixed silently.
func validateRoleRun(role *Role, rolesManifest *RoleManifest, declared CVMap) validation.ErrorList {
	allErrs := validation.ErrorList{}

	if role.Run == nil {
		return append(allErrs, validation.Required(
			fmt.Sprintf("roles[%s].run", role.Name), ""))
	}

	allErrs = append(allErrs, normalizeFlightStage(role)...)
	allErrs = append(allErrs, validateHealthCheck(role)...)
	allErrs = append(allErrs, validation.ValidateNonnegativeField(int64(role.Run.Memory),
		fmt.Sprintf("roles[%s].run.memory", role.Name))...)
	allErrs = append(allErrs, validation.ValidateNonnegativeField(int64(role.Run.VirtualCPUs),
		fmt.Sprintf("roles[%s].run.virtual-cpus", role.Name))...)

	for i := range role.Run.ExposedPorts {
		if role.Run.ExposedPorts[i].Name == "" {
			allErrs = append(allErrs, validation.Required(
				fmt.Sprintf("roles[%s].run.exposed-ports.name", role.Name), ""))
		}

		allErrs = append(allErrs, validation.ValidatePortRange(role.Run.ExposedPorts[i].External,
			fmt.Sprintf("roles[%s].run.exposed-ports[%s].external", role.Name, role.Run.ExposedPorts[i].Name))...)
		allErrs = append(allErrs, validation.ValidatePortRange(role.Run.ExposedPorts[i].Internal,
			fmt.Sprintf("roles[%s].run.exposed-ports[%s].internal", role.Name, role.Run.ExposedPorts[i].Name))...)

		allErrs = append(allErrs, validation.ValidateProtocol(role.Run.ExposedPorts[i].Protocol,
			fmt.Sprintf("roles[%s].run.exposed-ports[%s].protocol", role.Name, role.Run.ExposedPorts[i].Name))...)
	}

	if len(role.Run.Environment) == 0 {
		return allErrs
	}

	if role.Type == RoleTypeDocker {
		// The environment variables used by docker roles must
		// all be declared, report those which are not.

		for _, envVar := range role.Run.Environment {
			if _, ok := declared[envVar]; ok {
				continue
			}

			allErrs = append(allErrs, validation.NotFound(
				fmt.Sprintf("roles[%s].run.env", role.Name),
				fmt.Sprintf("No variable declaration of '%s'", envVar)))
		}
	} else {
		// Bosh roles must not provide environment variables.

		allErrs = append(allErrs, validation.Forbidden(
			fmt.Sprintf("roles[%s].run.env", role.Name),
			"Non-docker role declares bogus parameters"))
	}

	return allErrs
}

// validateHealthCheck reports all roles with conflicting health
// checks.
func validateHealthCheck(role *Role) validation.ErrorList {
	allErrs := validation.ErrorList{}

	// Ensure that we don't have conflicting health checks
	if role.Run.HealthCheck != nil {
		checks := make([]string, 0, 3)

		if role.Run.HealthCheck.URL != "" {
			checks = append(checks, "url")
		}
		if len(role.Run.HealthCheck.Command) > 0 {
			checks = append(checks, "command")
		}
		if role.Run.HealthCheck.Port != 0 {
			checks = append(checks, "port")
		}
		if len(checks) != 1 {
			allErrs = append(allErrs, validation.Invalid(
				fmt.Sprintf("roles[%s].run.healthcheck", role.Name),
				checks, "Expected exactly one of url, command, or port"))
		}
	}

	return allErrs
}

// normalizeFlightStage reports roles with a bad flightstage, and
// fixes all roles without a flight stage to use the default
// ('flight').
func normalizeFlightStage(role *Role) validation.ErrorList {
	allErrs := validation.ErrorList{}

	// Normalize flight stage
	switch role.Run.FlightStage {
	case "":
		role.Run.FlightStage = FlightStageFlight
	case FlightStagePreFlight:
	case FlightStageFlight:
	case FlightStagePostFlight:
	case FlightStageManual:
	default:
		allErrs = append(allErrs, validation.Invalid(
			fmt.Sprintf("roles[%s].run.flight-stage", role.Name),
			role.Run.FlightStage,
			"Expected one of flight, manual, post-flight, or pre-flight"))
	}

	return allErrs
}

// validateNonTemplates tests whether the global templates are
// constant or not. It reports the contant templates as errors (They
// should be opinions).
func validateNonTemplates(roleManifest *RoleManifest) validation.ErrorList {
	allErrs := validation.ErrorList{}

	// Iterate over the global templates, extract the used
	// variables. Report all templates not using any variable.

	for property, template := range roleManifest.Configuration.Templates {
		varsInTemplate, err := parseTemplate(template)
		if err != nil {
			// Ignore bad template, cannot have sensible
			// variable references
			continue
		}

		if len(varsInTemplate) == 0 {
			allErrs = append(allErrs, validation.Invalid("configuration.templates",
				template,
				fmt.Sprintf("Using '%s' as a constant", property)))
		}
	}

	return allErrs
}

// IsDevRole tests if the role is tagged for development, or not. It
// returns true for development-roles, and false otherwise.
func (r *Role) IsDevRole() bool {
	for _, tag := range r.Tags {
		switch tag {
		case "dev-only":
			return true
		}
	}
	return false
}
