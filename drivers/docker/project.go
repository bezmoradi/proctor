package docker

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	redacted     = "[redacted]"
	hostPath     = "[host path]"
	outsideScope = "[outside scope]"
	sourceSocket = "docker_socket"
	sourceHost   = "host_path"
)

var secretWords = []string{"password", "passwd", "secret", "token", "api_key", "apikey", "credential", "private_key"}

var hostPathLabels = map[string]bool{
	"com.docker.compose.project.working_dir":      true,
	"com.docker.compose.project.config_files":     true,
	"com.docker.compose.project.environment_file": true,
}

type skip struct{}

func (*skip) UnmarshalJSON([]byte) error { return nil }

type containerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

type networkSummary struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

type volumeList struct {
	Volumes []volumeSummary `json:"Volumes"`
}

type volumeSummary struct {
	Name    string            `json:"Name"`
	Driver  string            `json:"Driver"`
	Scope   string            `json:"Scope"`
	Labels  map[string]string `json:"Labels"`
	Options volumeOptions     `json:"Options"`
}

type volumeOptions struct {
	keys        []string
	typeName    string
	bind        bool
	bindsSocket bool
}

func (o *volumeOptions) UnmarshalJSON(b []byte) error {
	*o = volumeOptions{}
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	if d, isDelim := t.(json.Delim); !isDelim || d != '{' {
		return errors.New("volume options aren't an object")
	}
	var mountOptions, device string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, isText := t.(string)
		if !isText {
			return errors.New("a volume option has no name")
		}
		o.keys = append(o.keys, key)
		var into any = &skip{}
		switch key {
		case "type":
			into = &o.typeName
		case "o":
			into = &mountOptions
		case "device":
			into = &device
		}
		if err := dec.Decode(into); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	o.bind = device != "" && (o.typeName == "none" || strings.Contains(mountOptions, "bind"))
	o.bindsSocket = o.bind && socketSource("bind", device)
	return nil
}

type containerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		Dead       bool   `json:"Dead"`
		ExitCode   int    `json:"ExitCode"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount    int    `json:"RestartCount"`
	AppArmorProfile string `json:"AppArmorProfile"`
	ExecIDs         []skip `json:"ExecIDs"`
	Config          struct {
		Image  string            `json:"Image"`
		User   string            `json:"User"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		ReadonlyRootfs  bool                     `json:"ReadonlyRootfs"`
		Privileged      bool                     `json:"Privileged"`
		PublishAllPorts bool                     `json:"PublishAllPorts"`
		OomKillDisable  *bool                    `json:"OomKillDisable"`
		Init            *bool                    `json:"Init"`
		CapAdd          []string                 `json:"CapAdd"`
		CapDrop         []string                 `json:"CapDrop"`
		SecurityOpt     []string                 `json:"SecurityOpt"`
		GroupAdd        []string                 `json:"GroupAdd"`
		DNS             []string                 `json:"Dns"`
		DNSOptions      []string                 `json:"DnsOptions"`
		DNSSearch       []string                 `json:"DnsSearch"`
		ExtraHosts      []string                 `json:"ExtraHosts"`
		Runtime         string                   `json:"Runtime"`
		NetworkMode     string                   `json:"NetworkMode"`
		IpcMode         string                   `json:"IpcMode"`
		PidMode         string                   `json:"PidMode"`
		UTSMode         string                   `json:"UTSMode"`
		UsernsMode      string                   `json:"UsernsMode"`
		CgroupnsMode    string                   `json:"CgroupnsMode"`
		Memory          int64                    `json:"Memory"`
		MemorySwap      int64                    `json:"MemorySwap"`
		NanoCPUs        int64                    `json:"NanoCpus"`
		ShmSize         int64                    `json:"ShmSize"`
		OomScoreAdj     int                      `json:"OomScoreAdj"`
		PidsLimit       *int64                   `json:"PidsLimit"`
		Devices         []skip                   `json:"Devices"`
		DeviceRequests  []skip                   `json:"DeviceRequests"`
		Sysctls         map[string]string        `json:"Sysctls"`
		PortBindings    map[string][]portBinding `json:"PortBindings"`
		RestartPolicy   struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		LogConfig struct {
			Type string `json:"Type"`
		} `json:"LogConfig"`
		Mounts []struct {
			Type          string `json:"Type"`
			Target        string `json:"Target"`
			VolumeOptions *struct {
				Subpath string `json:"Subpath"`
			} `json:"VolumeOptions"`
		} `json:"Mounts"`
		Tmpfs map[string]skip `json:"Tmpfs"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			NetworkID string `json:"NetworkID"`
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type networkInspect struct {
	ID         string            `json:"Id"`
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	Internal   bool              `json:"Internal"`
	Attachable bool              `json:"Attachable"`
	EnableIPv6 bool              `json:"EnableIPv6"`
	Labels     map[string]string `json:"Labels"`
	Options    map[string]string `json:"Options"`
	IPAM       struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
	Containers map[string]struct {
		Name        string `json:"Name"`
		IPv4Address string `json:"IPv4Address"`
	} `json:"Containers"`
}

type pair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ref struct {
	Name    *string `json:"name"`
	Address *string `json:"ipv4_address"`
	InScope bool    `json:"in_scope"`
}

type containerState struct {
	Status       string  `json:"status"`
	Running      bool    `json:"running"`
	Paused       bool    `json:"paused"`
	Restarting   bool    `json:"restarting"`
	Dead         bool    `json:"dead"`
	OOMKilled    bool    `json:"oom_killed"`
	ExitCode     int     `json:"exit_code"`
	Health       *string `json:"health"`
	RestartCount int     `json:"restart_count"`
}

type port struct {
	ContainerPort string `json:"container_port"`
	HostIP        string `json:"host_ip"`
	HostPort      string `json:"host_port"`
}

type restartPolicy struct {
	Name              string `json:"name"`
	MaximumRetryCount int    `json:"maximum_retry_count"`
}

type mount struct {
	Type        string  `json:"type"`
	Destination string  `json:"destination"`
	ReadOnly    bool    `json:"read_only"`
	Volume      *string `json:"volume"`
	Subpath     *string `json:"subpath"`
	Source      *string `json:"source"`
}

type containerFact struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Image           string         `json:"image"`
	ImageRef        string         `json:"image_ref"`
	State           containerState `json:"state"`
	User            string         `json:"user"`
	Labels          []pair         `json:"labels"`
	ReadonlyRootfs  bool           `json:"readonly_rootfs"`
	Privileged      bool           `json:"privileged"`
	Init            bool           `json:"init"`
	CapAdd          []string       `json:"cap_add"`
	CapDrop         []string       `json:"cap_drop"`
	SecurityOpt     []string       `json:"security_opt"`
	AppArmorProfile string         `json:"apparmor_profile"`
	Runtime         string         `json:"runtime"`
	Memory          int64          `json:"memory"`
	MemorySwap      int64          `json:"memory_swap"`
	NanoCPUs        int64          `json:"nano_cpus"`
	PidsLimit       int64          `json:"pids_limit"`
	OomScoreAdj     int            `json:"oom_score_adj"`
	OomKillDisable  bool           `json:"oom_kill_disable"`
	NetworkMode     string         `json:"network_mode"`
	IpcMode         string         `json:"ipc_mode"`
	PidMode         string         `json:"pid_mode"`
	UTSMode         string         `json:"uts_mode"`
	UsernsMode      string         `json:"userns_mode"`
	CgroupnsMode    string         `json:"cgroupns_mode"`
	ShmSize         int64          `json:"shm_size"`
	GroupAdd        []string       `json:"group_add"`
	Devices         int            `json:"devices"`
	DeviceRequests  int            `json:"device_requests"`
	Sysctls         []pair         `json:"sysctls"`
	DNS             []string       `json:"dns"`
	DNSOptions      []string       `json:"dns_options"`
	DNSSearch       []string       `json:"dns_search"`
	ExtraHosts      []string       `json:"extra_hosts"`
	Ports           []port         `json:"ports"`
	PublishAllPorts bool           `json:"publish_all_ports"`
	RestartPolicy   restartPolicy  `json:"restart_policy"`
	LogDriver       string         `json:"log_driver"`
	Networks        []ref          `json:"networks"`
	Mounts          []mount        `json:"mounts"`
	Tmpfs           []string       `json:"tmpfs"`
	DockerSocket    bool           `json:"docker_socket"`
	Execs           int            `json:"execs"`
}

type ipam struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
}

type networkFact struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Scope      string `json:"scope"`
	Internal   bool   `json:"internal"`
	Attachable bool   `json:"attachable"`
	EnableIPv6 bool   `json:"enable_ipv6"`
	Labels     []pair `json:"labels"`
	Options    []pair `json:"options"`
	IPAM       []ipam `json:"ipam"`
	Members    []ref  `json:"members"`
}

type volumeFact struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Scope      string   `json:"scope"`
	Labels     []pair   `json:"labels"`
	TypeOption string   `json:"type_option"`
	OptionKeys []string `json:"option_keys"`
}

type visible struct {
	containerIDs   map[string]string
	containerNames map[string]string
	networkIDs     map[string]string
	networkNames   map[string]string
	volumes        map[string]volumeOptions
	volumeDrivers  map[string]string
}

func (v *visible) container(idOrName string) (string, bool) {
	if name, ok := v.containerIDs[idOrName]; ok {
		return name, true
	}
	if _, ok := v.containerNames[idOrName]; ok {
		return idOrName, true
	}
	return "", false
}

func (v *visible) network(idOrName string) (string, bool) {
	if name, ok := v.networkIDs[idOrName]; ok {
		return name, true
	}
	if _, ok := v.networkNames[idOrName]; ok {
		return idOrName, true
	}
	return "", false
}

func projectContainer(c *containerInspect, v *visible) (containerFact, error) {
	h := &c.HostConfig
	f := containerFact{
		ID:              c.ID,
		Name:            strings.TrimPrefix(c.Name, "/"),
		Image:           c.Image,
		ImageRef:        c.Config.Image,
		User:            c.Config.User,
		Labels:          labelPairs(c.Config.Labels),
		ReadonlyRootfs:  h.ReadonlyRootfs,
		Privileged:      h.Privileged,
		Init:            h.Init != nil && *h.Init,
		CapAdd:          sortedTexts(h.CapAdd),
		CapDrop:         sortedTexts(h.CapDrop),
		SecurityOpt:     sortedTexts(h.SecurityOpt),
		AppArmorProfile: c.AppArmorProfile,
		Runtime:         h.Runtime,
		Memory:          h.Memory,
		MemorySwap:      h.MemorySwap,
		NanoCPUs:        h.NanoCPUs,
		OomScoreAdj:     h.OomScoreAdj,
		OomKillDisable:  h.OomKillDisable != nil && *h.OomKillDisable,
		NetworkMode:     maskNetworkMode(h.NetworkMode, v),
		IpcMode:         maskContainerMode(h.IpcMode, v),
		PidMode:         maskContainerMode(h.PidMode, v),
		UTSMode:         h.UTSMode,
		UsernsMode:      h.UsernsMode,
		CgroupnsMode:    h.CgroupnsMode,
		ShmSize:         h.ShmSize,
		GroupAdd:        sortedTexts(h.GroupAdd),
		Devices:         len(h.Devices),
		DeviceRequests:  len(h.DeviceRequests),
		Sysctls:         plainPairs(h.Sysctls),
		DNS:             sortedTexts(h.DNS),
		DNSOptions:      sortedTexts(h.DNSOptions),
		DNSSearch:       sortedTexts(h.DNSSearch),
		ExtraHosts:      sortedTexts(h.ExtraHosts),
		Ports:           ports(h.PortBindings),
		PublishAllPorts: h.PublishAllPorts,
		RestartPolicy:   restartPolicy{Name: h.RestartPolicy.Name, MaximumRetryCount: h.RestartPolicy.MaximumRetryCount},
		LogDriver:       h.LogConfig.Type,
		Execs:           len(c.ExecIDs),
	}
	f.State = containerState{
		Status: c.State.Status, Running: c.State.Running, Paused: c.State.Paused, Restarting: c.State.Restarting,
		Dead: c.State.Dead, OOMKilled: c.State.OOMKilled, ExitCode: c.State.ExitCode, RestartCount: c.RestartCount,
	}
	if c.State.Health != nil && c.State.Health.Status != "" && c.State.Health.Status != "none" {
		s := c.State.Health.Status
		f.State.Health = &s
	}
	if h.PidsLimit != nil {
		f.PidsLimit = *h.PidsLimit
	}
	f.Networks = make([]ref, 0, len(c.NetworkSettings.Networks))
	for name, n := range c.NetworkSettings.Networks {
		_, idInScope := v.networkIDs[n.NetworkID]
		_, nameInScope := v.networkNames[name]
		if (n.NetworkID != "" && idInScope) || (n.NetworkID == "" && nameInScope) {
			f.Networks = append(f.Networks, named(name, n.IPAddress))
			continue
		}
		f.Networks = append(f.Networks, ref{})
	}
	sortRefs(f.Networks)
	subpaths := map[string]string{}
	for _, m := range h.Mounts {
		if m.VolumeOptions != nil && m.VolumeOptions.Subpath != "" {
			subpaths[m.Target] = m.VolumeOptions.Subpath
		}
	}
	tmpfs := map[string]bool{}
	for dst := range h.Tmpfs {
		tmpfs[dst] = true
	}
	f.Mounts = []mount{}
	for _, m := range c.Mounts {
		if m.Type == "tmpfs" {
			tmpfs[m.Destination] = true
			continue
		}
		out := mount{Type: m.Type, Destination: m.Destination, ReadOnly: !m.RW}
		if sp, ok := subpaths[m.Destination]; ok {
			out.Subpath = &sp
		}
		switch m.Type {
		case "volume":
			socket, err := volumeSocket(f.Name, m.Name, m.Destination, out.Subpath, v)
			if err != nil {
				return containerFact{}, err
			}
			if _, in := v.volumes[m.Name]; in {
				name := m.Name
				out.Volume = &name
			}
			if socket {
				src := sourceSocket
				out.Source = &src
				f.DockerSocket = true
			}
		case "bind", "npipe":
			src := sourceHost
			if bindsDockerSocket(m.Type, m.Source, m.Destination) {
				src = sourceSocket
				f.DockerSocket = true
			}
			out.Source = &src
		}
		f.Mounts = append(f.Mounts, out)
	}
	slices.SortFunc(f.Mounts, func(a, b mount) int { return cmp.Compare(a.Destination, b.Destination) })
	f.Tmpfs = append([]string{}, sortedKeys(tmpfs)...)
	return f, nil
}

func volumeSocket(container, name, destination string, subpath *string, v *visible) (bool, error) {
	if socketDestination("volume", destination) {
		return true, nil
	}
	o, in := v.volumes[name]
	switch {
	case !in:
		return false, protocol.Lastingf(protocol.CauseUnsupportedResult, "container %s mounts a volume outside the scope at %s, so the driver can't read its options or tell whether it reaches Docker's socket; give the volume a label of the scope (DKR-07)", container, oneLine(destination))
	case v.volumeDrivers[name] != "local":
		return false, protocol.Lastingf(protocol.CauseUnsupportedResult, "container %s mounts volume %s at %s, whose driver %s isn't local, so the driver can't tell whether it reaches Docker's socket (DKR-07)", container, name, oneLine(destination), oneLine(v.volumeDrivers[name]))
	case !o.bind:
		return false, nil
	case o.bindsSocket:
		return true, nil
	case subpath != nil:
		return subpathSocket(*subpath), nil
	}
	return false, nil
}

func subpathSocket(subpath string) bool {
	p := cleanPath("/" + subpath)
	return dockerSocketName("bind", p) || path.Base(p) == ".docker" || strings.HasSuffix(p, "/.docker/run")
}

var socketDestinations = map[string]bool{
	"/var/run/docker.sock": true,
	"/run/docker.sock":     true,
}

var socketFolders = map[string]bool{
	"/":                  true,
	"/var":               true,
	"/var/run":           true,
	"/run":               true,
	"/run/host-services": true,
}

var userRunFolder = regexp.MustCompile(`^/run/user/[0-9]+$`)

const (
	desktopServices  = "/run/host-services/"
	desktopSSHAgent  = "ssh-auth.sock"
	desktopHostMount = "/host_mnt"
	macPrivate       = "/private"
)

func bindsDockerSocket(kind, source, destination string) bool {
	return socketDestination(kind, destination) || socketSource(kind, source)
}

func cleanPath(p string) string {
	return path.Clean(strings.ReplaceAll(p, `\`, "/"))
}

func socketDestination(kind, destination string) bool {
	dst := cleanPath(destination)
	return socketDestinations[dst] || dockerSocketName(kind, dst)
}

func hostSource(source string) string {
	src := cleanPath(source)
	if rest, ok := strings.CutPrefix(src, desktopHostMount); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		src = cmp.Or(rest, "/")
	}
	if src == macPrivate {
		return "/var"
	}
	if rest, ok := strings.CutPrefix(src, macPrivate); ok && (rest == "/var" || strings.HasPrefix(rest, "/var/")) {
		return rest
	}
	return src
}

func socketSource(kind, source string) bool {
	src := hostSource(source)
	switch {
	case socketFolders[src], userRunFolder.MatchString(src):
		return true
	case path.Base(src) == ".docker", strings.HasSuffix(src, "/.docker/run"):
		return true
	case strings.HasPrefix(src, desktopServices) && strings.HasSuffix(src, ".sock") && path.Base(src) != desktopSSHAgent:
		return true
	}
	return dockerSocketName(kind, src)
}

func dockerSocketName(kind, p string) bool {
	base := strings.ToLower(path.Base(p))
	return strings.HasPrefix(base, "docker") && (strings.HasSuffix(base, ".sock") || kind == "npipe")
}

func maskContainerMode(mode string, v *visible) string {
	target, ok := strings.CutPrefix(mode, "container:")
	if !ok {
		return mode
	}
	if name, in := v.container(target); in {
		return "container:" + name
	}
	return "container:" + outsideScope
}

func maskNetworkMode(mode string, v *visible) string {
	switch mode {
	case "", "default", "bridge", "host", "none":
		return mode
	}
	if strings.HasPrefix(mode, "container:") {
		return maskContainerMode(mode, v)
	}
	if name, in := v.network(mode); in {
		return name
	}
	return outsideScope
}

func projectNetwork(n *networkInspect, v *visible) networkFact {
	f := networkFact{
		ID:         n.ID,
		Name:       n.Name,
		Driver:     n.Driver,
		Scope:      n.Scope,
		Internal:   n.Internal,
		Attachable: n.Attachable,
		EnableIPv6: n.EnableIPv6,
		Labels:     labelPairs(n.Labels),
		Options:    labelPairs(n.Options),
		IPAM:       []ipam{},
		Members:    make([]ref, 0, len(n.Containers)),
	}
	for _, c := range n.IPAM.Config {
		f.IPAM = append(f.IPAM, ipam{Subnet: c.Subnet, Gateway: c.Gateway})
	}
	slices.SortFunc(f.IPAM, func(a, b ipam) int { return cmp.Or(cmp.Compare(a.Subnet, b.Subnet), cmp.Compare(a.Gateway, b.Gateway)) })
	for id, m := range n.Containers {
		if name, in := v.containerIDs[id]; in {
			f.Members = append(f.Members, named(name, m.IPv4Address))
			continue
		}
		f.Members = append(f.Members, ref{})
	}
	sortRefs(f.Members)
	return f
}

func projectVolume(s *volumeSummary) volumeFact {
	return volumeFact{
		Name:       s.Name,
		Driver:     s.Driver,
		Scope:      s.Scope,
		Labels:     labelPairs(s.Labels),
		TypeOption: s.Options.typeName,
		OptionKeys: sortedTexts(s.Options.keys),
	}
}

func named(name, address string) ref {
	n := name
	a := address
	if p, err := netip.ParsePrefix(address); err == nil {
		a = p.Addr().String()
	}
	return ref{Name: &n, Address: &a, InScope: true}
}

func sortRefs(refs []ref) {
	slices.SortFunc(refs, func(a, b ref) int {
		switch {
		case a.Name == nil && b.Name == nil:
			return 0
		case a.Name == nil:
			return 1
		case b.Name == nil:
			return -1
		}
		return cmp.Or(cmp.Compare(*a.Name, *b.Name), cmp.Compare(*a.Address, *b.Address))
	})
}

func ports(bindings map[string][]portBinding) []port {
	out := []port{}
	for cp, list := range bindings {
		if len(list) == 0 {
			out = append(out, port{ContainerPort: cp})
			continue
		}
		for _, b := range list {
			out = append(out, port{ContainerPort: cp, HostIP: b.HostIP, HostPort: b.HostPort})
		}
	}
	slices.SortFunc(out, func(a, b port) int {
		return cmp.Or(cmp.Compare(a.ContainerPort, b.ContainerPort), cmp.Compare(a.HostPort, b.HostPort), cmp.Compare(a.HostIP, b.HostIP))
	})
	return out
}

func labelPairs(m map[string]string) []pair {
	out := make([]pair, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, pair{Key: k, Value: labelValue(k, m[k])})
	}
	return out
}

func labelValue(key, value string) string {
	if hostPathLabels[key] {
		return hostPath
	}
	lower := strings.ToLower(key)
	for _, w := range secretWords {
		if strings.Contains(lower, w) {
			return redacted
		}
	}
	return value
}

func plainPairs(m map[string]string) []pair {
	out := make([]pair, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, pair{Key: k, Value: m[k]})
	}
	return out
}

func sortedTexts(l []string) []string {
	out := append([]string{}, l...)
	slices.Sort(out)
	return out
}
