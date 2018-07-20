package proxmox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type (
	QemuDevices     map[int]map[string]interface{}
	QemuDevice      map[string]interface{}
	QemuDeviceParam []string
)

type ConfigQemu struct {
	Name         string      `json:"name"`
	Description  string      `json:"desc"`
	Onboot       int         `json:"onboot"`
	Memory       int         `json:"memory"`
	Storage      string      `json:"storage"`
	QemuOs       string      `json:"os"`
	QemuCores    int         `json:"cores"`
	QemuSockets  int         `json:"sockets"`
	QemuIso      string      `json:"iso"`
	QemuDisks    QemuDevices `json:"disk"`
	QemuNetworks QemuDevices `json:"network"`
	FullClone    *int        `json:"fullclone"`
	// Deprecated.
	QemuNicModel string  `json:"nic"`
	QemuBrige    string  `json:"bridge"`
	QemuVlanTag  int     `json:"vlan"`
	DiskSize     float64 `json:"diskGB"`
}

func (config ConfigQemu) CreateVm(vmr *VmRef, client *Client) (err error) {
	vmr.SetVmType("qemu")

	params := map[string]interface{}{
		"vmid":        strconv.Itoa(vmr.vmId),
		"name":        config.Name,
		"onboot":      config.Onboot,
		"ide2":        config.QemuIso + ",media=cdrom",
		"ostype":      config.QemuOs,
		"sockets":     strconv.Itoa(config.QemuSockets),
		"cores":       strconv.Itoa(config.QemuCores),
		"cpu":         "host",
		"memory":      strconv.Itoa(config.Memory),
		"description": config.Description,
	}

	// Create network config.
	config.CreateQemuDisksParams(params)

	// Create network config.
	config.CreateQemuNetworksParams(params)

	_, err = client.CreateQemuVm(vmr.node, params)
	return
}

/*

CloneVm
Example: Request

nodes/proxmox1-xx/qemu/1012/clone

newid:145
name:tf-clone1
target:proxmox1-xx
full:1
storage:xxx

*/
func (config ConfigQemu) CloneVm(sourceVmr *VmRef, vmr *VmRef, client *Client) (err error) {
	vmr.SetVmType("qemu")
	fullclone := "1"
	if config.FullClone != nil {
		fullclone = strconv.Itoa(*config.FullClone)
	}
	params := map[string]interface{}{
		"newid":   strconv.Itoa(vmr.vmId),
		"target":  vmr.node,
		"name":    config.Name,
		"storage": config.Storage,
		"full":    fullclone,
	}
	_, err = client.CloneQemuVm(sourceVmr, params)
	if err != nil {
		return
	}
	return config.UpdateConfig(vmr, client)
}

func (config ConfigQemu) UpdateConfig(vmr *VmRef, client *Client) (err error) {
	configParams := map[string]interface{}{
		"description": config.Description,
		"onboot":      config.Onboot,
		"sockets":     strconv.Itoa(config.QemuSockets),
		"cores":       strconv.Itoa(config.QemuCores),
		"memory":      strconv.Itoa(config.Memory),
	}

	// Create network config.
	config.CreateQemuDisksParams(configParams)

	// Create network config.
	config.CreateQemuNetworksParams(configParams)

	_, err = client.SetVmConfig(vmr, configParams)
	return err
}

func NewConfigQemuFromJson(io io.Reader) (config *ConfigQemu, err error) {
	config = &ConfigQemu{QemuVlanTag: -1}
	err = json.NewDecoder(io).Decode(config)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	log.Println(config)
	return
}

//var rxStorage = regexp.MustCompile("(.*?):.*?,size=(\\d+)G")
var rxIso = regexp.MustCompile("(.*?),media")

func NewConfigQemuFromApi(vmr *VmRef, client *Client) (config *ConfigQemu, err error) {
	var vmConfig map[string]interface{}
	for ii := 0; ii < 3; ii++ {
		vmConfig, err = client.GetVmConfig(vmr)
		if err != nil {
			log.Fatal(err)
			return nil, err
		}
		// this can happen:
		// {"data":{"lock":"clone","digest":"eb54fb9d9f120ba0c3bdf694f73b10002c375c38","description":" qmclone temporary file\n"}})
		if vmConfig["lock"] == nil {
			break
		} else {
			time.Sleep(8 * time.Second)
		}
	}

	if vmConfig["lock"] != nil {
		return nil, errors.New("vm locked, could not obtain config")
	}

	// vmConfig Sample: map[ cpu:host
	// net0:virtio=62:DF:XX:XX:XX:XX,bridge=vmbr0
	// ide2:local:iso/xxx-xx.iso,media=cdrom memory:2048
	// smbios1:uuid=8b3bf833-aad8-4545-xxx-xxxxxxx digest:aa6ce5xxxxx1b9ce33e4aaeff564d4 sockets:1
	// name:terraform-ubuntu1404-template bootdisk:virtio0
	// virtio0:ProxmoxxxxISCSI:vm-1014-disk-2,size=4G
	// description:Base image
	// cores:2 ostype:l26

	fullclone := 1
	if vmConfig["fullclone"] != nil {
		fullclone = int(vmConfig["fullclone"].(float64))
	}
	description := ""
	if vmConfig["description"] != nil {
		description = vmConfig["description"].(string)
	}

	config = &ConfigQemu{
		Name:         vmConfig["name"].(string),
		Description:  strings.TrimSpace(description),
		Onboot:       int(vmConfig["onboot"].(float64)),
		QemuOs:       vmConfig["ostype"].(string),
		Memory:       int(vmConfig["memory"].(float64)),
		QemuCores:    int(vmConfig["cores"].(float64)),
		QemuSockets:  int(vmConfig["sockets"].(float64)),
		QemuVlanTag:  -1,
		FullClone:    &fullclone,
		QemuDisks:    QemuDevices{},
		QemuNetworks: QemuDevices{},
	}

	if vmConfig["ide2"] != nil {
		isoMatch := rxIso.FindStringSubmatch(vmConfig["ide2"].(string))
		config.QemuIso = isoMatch[1]
	}

	// Disks.
	diskNameRe := regexp.MustCompile(`virtio\d+`)
	diskNames := []string{}

	for k, _ := range vmConfig {
		if diskName := diskNameRe.FindStringSubmatch(k); len(diskName) > 0 {
			diskNames = append(diskNames, diskName[0])
		}
	}

	for _, diskName := range diskNames {
		diskIDRe := regexp.MustCompile(`\d+`)
		diskTypeRe := regexp.MustCompile(`\D+`)
		diskConfStr := vmConfig[diskName]
		diskConfList := strings.Split(diskConfStr.(string), ",")

		//
		id := diskIDRe.FindStringSubmatch(diskName)
		diskID, _ := strconv.Atoi(id[0])
		diskType := diskTypeRe.FindStringSubmatch(diskName)[0]
		diskStorage := strings.Split(diskConfList[0], ":")[0]

		//
		diskConfMap := QemuDevice{
			"type":    diskType,
			"storage": diskStorage,
		}

		// Add rest of device config.
		diskConfMap.updateDeviceConfig(diskConfList[1:])

		// And device config to disks map.
		if len(diskConfMap) > 0 {
			config.QemuDisks[diskID] = diskConfMap
		}
	}

	// Networks.
	nicNameRe := regexp.MustCompile(`net\d+`)
	nicNames := []string{}

	for k, _ := range vmConfig {
		if nicName := nicNameRe.FindStringSubmatch(k); len(nicName) > 0 {
			nicNames = append(nicNames, nicName[0])
		}
	}

	for _, nicName := range nicNames {
		nicIDRe := regexp.MustCompile(`\d+`)
		nicConfStr := vmConfig[nicName]
		nicConfList := strings.Split(nicConfStr.(string), ",")

		//
		id := nicIDRe.FindStringSubmatch(nicName)
		nicID, _ := strconv.Atoi(id[0])

		// Remove MAC address, and add type only.
		nicConfMap := QemuDevice{
			"type": strings.Split(nicConfList[0], "=")[0],
		}

		// Add rest of device config.
		nicConfMap.updateDeviceConfig(nicConfList[1:])

		// And device config to networks.
		if len(nicConfMap) > 0 {
			config.QemuNetworks[nicID] = nicConfMap
		}
	}

	return
}

// Useful waiting for ISO install to complete
func WaitForShutdown(vmr *VmRef, client *Client) (err error) {
	for ii := 0; ii < 100; ii++ {
		vmState, err := client.GetVmState(vmr)
		if err != nil {
			log.Print("Wait error:")
			log.Println(err)
		} else if vmState["status"] == "stopped" {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return errors.New("Not shutdown within wait time")
}

// This is because proxmox create/config API won't let us make usernet devices
func SshForwardUsernet(vmr *VmRef, client *Client) (sshPort string, err error) {
	vmState, err := client.GetVmState(vmr)
	if err != nil {
		return "", err
	}
	if vmState["status"] == "stopped" {
		return "", errors.New("VM must be running first")
	}
	sshPort = strconv.Itoa(vmr.VmId() + 22000)
	_, err = client.MonitorCmd(vmr, "netdev_add user,id=net1,hostfwd=tcp::"+sshPort+"-:22")
	if err != nil {
		return "", err
	}
	_, err = client.MonitorCmd(vmr, "device_add virtio-net-pci,id=net1,netdev=net1,addr=0x13")
	if err != nil {
		return "", err
	}
	return
}

// device_del net1
// netdev_del net1
func RemoveSshForwardUsernet(vmr *VmRef, client *Client) (err error) {
	vmState, err := client.GetVmState(vmr)
	if err != nil {
		return err
	}
	if vmState["status"] == "stopped" {
		return errors.New("VM must be running first")
	}
	_, err = client.MonitorCmd(vmr, "device_del net1")
	if err != nil {
		return err
	}
	_, err = client.MonitorCmd(vmr, "netdev_del net1")
	if err != nil {
		return err
	}
	return nil
}

func MaxVmId(client *Client) (max int, err error) {
	resp, err := client.GetVmList()
	vms := resp["data"].([]interface{})
	max = 0
	for vmii := range vms {
		vm := vms[vmii].(map[string]interface{})
		vmid := int(vm["vmid"].(float64))
		if vmid > max {
			max = vmid
		}
	}
	return
}

func SendKeysString(vmr *VmRef, client *Client, keys string) (err error) {
	vmState, err := client.GetVmState(vmr)
	if err != nil {
		return err
	}
	if vmState["status"] == "stopped" {
		return errors.New("VM must be running first")
	}
	for _, r := range keys {
		c := string(r)
		lower := strings.ToLower(c)
		if c != lower {
			c = "shift-" + lower
		} else {
			switch c {
			case "!":
				c = "shift-1"
			case "@":
				c = "shift-2"
			case "#":
				c = "shift-3"
			case "$":
				c = "shift-4"
			case "%%":
				c = "shift-5"
			case "^":
				c = "shift-6"
			case "&":
				c = "shift-7"
			case "*":
				c = "shift-8"
			case "(":
				c = "shift-9"
			case ")":
				c = "shift-0"
			case "_":
				c = "shift-minus"
			case "+":
				c = "shift-equal"
			case " ":
				c = "spc"
			case "/":
				c = "slash"
			case "\\":
				c = "backslash"
			case ",":
				c = "comma"
			case "-":
				c = "minus"
			case "=":
				c = "equal"
			case ".":
				c = "dot"
			case "?":
				c = "shift-slash"
			}
		}
		_, err = client.MonitorCmd(vmr, "sendkey "+c)
		if err != nil {
			return err
		}
		time.Sleep(100)
	}
	return nil
}

func inArray(arr []string, str string) bool {
	for _, elem := range arr {
		if elem == str {
			return true
		}
	}

	return false
}

func (c ConfigQemu) CreateQemuNetworksParams(params map[string]interface{}) error {

	// For backward compatibility.
	if len(c.QemuNetworks) == 0 && len(c.QemuNicModel) > 0 {
		deprecatedStyleMap := QemuDevice{
			"type":   c.QemuNicModel,
			"bridge": c.QemuBrige,
		}

		if c.QemuVlanTag > 0 {
			deprecatedStyleMap["tag"] = strconv.Itoa(c.QemuVlanTag)
		}

		c.QemuNetworks[0] = deprecatedStyleMap
	}

	// For new style with multi net device.
	for nicID, nicConfMap := range c.QemuNetworks {

		// Set Nic name.
		qemuNicName := "net" + strconv.Itoa(nicID)

		// Set Nic type.
		deviceType := nicConfMap["type"].(string)
		nicConfType := strings.Split(deviceType, "=")[0]
		nicConfParam := QemuDeviceParam{nicConfType}

		// Set bridge if not nat.
		if nicConfMap["bridge"].(string) != "nat" {
			bridge := fmt.Sprintf("bridge=%v", nicConfMap["bridge"])
			nicConfParam = append(nicConfParam, bridge)
		}

		// Keys that are not used as real/direct conf.
		ignoredKeys := []string{"id", "type", "bridge"}

		// Rest of config.
		nicConfParam = nicConfParam.createDeviceParam(nicConfMap, ignoredKeys)

		// Add nic to Qemu prams.
		params[qemuNicName] = strings.Join(nicConfParam, ",")
	}

	return nil
}

func (c ConfigQemu) CreateQemuDisksParams(params map[string]interface{}) error {

	// For backward compatibility.
	if len(c.QemuDisks) == 0 && len(c.Storage) > 0 {
		deprecatedStyleMap := QemuDevice{
			"storage": c.Storage,
			"size":    c.DiskSize,
		}

		c.QemuDisks[0] = deprecatedStyleMap
	}

	// For new style with multi disk device.
	for diskID, diskConfMap := range c.QemuDisks {

		// Set disk name.
		deviceType := diskConfMap["type"].(string)
		qemuDiskName := deviceType + strconv.Itoa(diskID)

		// Set disk storage.
		diskSize := diskConfMap["size"].(string)
		diskSizeGB := strings.Trim(diskSize, "G")
		diskStorage := fmt.Sprintf("%v:%v", diskConfMap["storage"], diskSizeGB)
		diskConfParam := QemuDeviceParam{diskStorage}

		// Set cache if not none (default).
		if diskConfMap["cache"].(string) != "none" {
			diskCache := fmt.Sprintf("cache=%v", diskConfMap["cache"])
			diskConfParam = append(diskConfParam, diskCache)
		}

		// Keys that are not used as real/direct conf.
		ignoredKeys := []string{"id", "type", "storage", "size", "cache"}

		// Rest of config.
		diskConfParam = diskConfParam.createDeviceParam(diskConfMap, ignoredKeys)

		// Add back to Qemu prams.
		params[qemuDiskName] = strings.Join(diskConfParam, ",")
	}

	return nil
}

func (p QemuDeviceParam) createDeviceParam(
	deviceConfMap QemuDevice,
	ignoredKeys []string,
) QemuDeviceParam {

	for key, value := range deviceConfMap {
		if ignored := inArray(ignoredKeys, key); !ignored {
			var confValue interface{}
			if sValue, ok := value.(string); ok && len(sValue) > 0 {
				confValue = sValue
			} else if iValue, ok := value.(int); ok && iValue > 0 {
				confValue = iValue
			}
			if confValue != nil {
				deviceConf := fmt.Sprintf("%v=%v", key, confValue)
				p = append(p, deviceConf)
			}
		}
	}

	return p
}

func (confMap QemuDevice) updateDeviceConfig(confList []string) error {
	// Add device config.
	for _, confs := range confList {
		conf := strings.Split(confs, "=")
		key := conf[0]
		value := conf[1]
		// Make sure to add value in right type because
		// all values are retruned as strings from Proxmox config.
		if iValue, err := strconv.ParseInt(value, 10, 64); err == nil {
			confMap[key] = int(iValue)
		} else if bValue, err := strconv.ParseBool(value); err == nil {
			confMap[key] = bValue
		} else {
			confMap[key] = value
		}
	}
	return nil
}
