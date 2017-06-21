package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"

	log "github.com/Sirupsen/logrus"
)

type vCenter struct {
	vCenterURL  *string
	dsName      *string
	networkName *string
	vSphereHost *string
}

type vcInternal struct {
	client       *govmomi.Client
	datastore    *object.Datastore
	dcFolders    *object.DatacenterFolders
	hostSystem   *object.HostSystem
	network      object.NetworkReference
	resourcePool *object.ResourcePool
}

type vmInstance struct {

	// Used with LinuxKit ISOs
	isoPath string
	// Used with a VMware VM template
	vmTemplate string

	// Used by InfraKit to tack group
	groupTag string

	// Folder that will store all InfraKit instances
	instanceFolder string
	// InfraKit vSphere instance settings
	annotation   string
	vmPrefix     string
	vmName       string
	persistent   string
	persistentSz int
	vCpus        int
	mem          int
	poweron      bool
	guestIP      bool
}

func vCenterConnect(vc *vCenter) (vcInternal, error) {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var internals vcInternal
	// Parse URL from string
	u, err := url.Parse(*vc.vCenterURL)
	if err != nil {
		return internals, errors.New("URL can't be parsed, ensure it is https://username:password/<address>/sdk")
	}

	// Connect and log in to ESX or vCenter
	internals.client, err = govmomi.NewClient(ctx, u, true)
	if err != nil {
		return internals, errors.New(fmt.Sprintf("Error logging into vCenter, check address and credentials %v", err))
	}
	return internals, nil
}

func setInternalStructures(vc *vCenter, internals *vcInternal) error {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a new finder that will discover the defaults and are looked for Networks/Datastores
	f := find.NewFinder(internals.client.Client, true)

	// Find one and only datacenter, not sure how VMware linked mode will work
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		return errors.New(fmt.Sprintf("No Datacenter instance could be found inside of vCenter %v", err))
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)

	// Find Datastore/Network
	internals.datastore, err = f.DatastoreOrDefault(ctx, *vc.dsName)
	if err != nil {
		return errors.New(fmt.Sprintf("Datastore [%s], could not be found", *vc.dsName))
	}

	internals.dcFolders, err = dc.Folders(ctx)
	if err != nil {
		return errors.New(fmt.Sprintf("Error locating default datacenter folder"))
	}

	// Set the host that the VM will be created on
	internals.hostSystem, err = f.HostSystemOrDefault(ctx, *vc.vSphereHost)
	if err != nil {
		return errors.New(fmt.Sprintf("vSphere host [%s], could not be found", *vc.vSphereHost))
	}

	// Find the resource pool attached to this host
	internals.resourcePool, err = internals.hostSystem.ResourcePool(ctx)
	if err != nil {
		return errors.New(fmt.Sprintf("Error locating default resource pool"))
	}
	return nil
}

func parseParameters(properties map[string]interface{}, p *plugin) (vmInstance, error) {

	var newInstance vmInstance

	log.Debugf("Building vCenter specific parameters")
	if *p.vC.vCenterURL == "" {
		if properties["vCenterURL"] == nil {
			return newInstance, errors.New("Environment variable VCURL or .yml vCenterURL must be set")
		} else {
			*p.vC.vCenterURL = properties["vCenterURL"].(string)
		}
	}

	if properties["Datastore"] == nil {
		return newInstance, errors.New("Property 'Datastore' must be set")
	} else {
		*p.vC.dsName = properties["Datastore"].(string)
		log.Debugf("Datastore set to %s", *p.vC.dsName)
	}

	if properties["Hostname"] == nil {
		return newInstance, errors.New("Property 'Hostname' must be set")
	} else {
		*p.vC.vSphereHost = properties["Hostname"].(string)
	}

	if properties["Network"] == nil {
		log.Warnf("The property 'Network' hasn't been set, no networks will be attached to VM")
	} else {
		*p.vC.networkName = properties["Network"].(string)
	}

	if properties["Annotation"] != nil {
		newInstance.annotation = properties["Annotation"].(string)
	}

	if properties["vmPrefix"] == nil {
		newInstance.vmPrefix = "vm"
	} else {
		newInstance.vmPrefix = properties["vmPrefix"].(string)
	}

	if properties["isoPath"] == nil {
		log.Debugf("The property 'isoPath' hasn't been set, no networks will be attached to VM")
	} else {
		newInstance.isoPath = properties["isoPath"].(string)
	}

	if properties["CPUs"] == nil {
		newInstance.vCpus = 1
	} else {
		newInstance.vCpus = int(properties["CPUs"].(float64))

	}

	if properties["Memory"] == nil {
		newInstance.mem = 512
	} else {
		newInstance.mem = int(properties["Memory"].(float64))
	}

	if properties["persistantSZ"] == nil {
		newInstance.persistentSz = 0
	} else {
		newInstance.persistentSz = int(properties["persistantSZ"].(float64))
	}

	return newInstance, nil
}

func findNetwork(vc *vCenter, internals *vcInternal) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a new finder that will discover the defaults and are looked for Networks/Datastores
	f := find.NewFinder(internals.client.Client, true)

	// Find one and only datacenter, not sure how VMware linked mode will work
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		log.Fatalf("No Datacenter instance could be found inside of vCenter %v", err)
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)

	if *vc.networkName != "" {
		internals.network, err = f.NetworkOrDefault(ctx, *vc.networkName)
		if err != nil {
			log.Fatalf("Network [%s], could not be found", *vc.networkName)
		}
	}
}

func findGroupInstances(p *plugin, groupName string) ([]*object.VirtualMachine, error) {

	if groupName == "" {
		return nil, errors.New("The tag infrakit.group was blank")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a new finder that will discover the defaults and are looked for Networks/Datastores
	f := find.NewFinder(p.vCenterInternals.client.Client, true)

	// Find one and only datacenter, not sure how VMware linked mode will work
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("No Datacenter instance could be found inside of vCenter %v", err))
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)
	vmList, err := f.VirtualMachineList(ctx, groupName+"/*")
	if err != nil {
		log.Errorf("3: %v", err)
	}

	// Fallback to searching ALL VMs (ESXi use-case)
	vmList, err = f.VirtualMachineList(ctx, "*")
	if err != nil {
		log.Errorf("4: %v", err)
	} //else {
	// 	for _, vmInstance := range vmList {
	//
	// }

	if len(vmList) == 0 {
		log.Errorf("No Virtual Machines found in Folder")
	}
	return vmList, nil
}

func createNewVMInstance(p *plugin, vm *vmInstance, groupID string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := types.VirtualMachineConfigSpec{
		Name:       vm.vmName,
		GuestId:    "otherLinux64Guest",
		Files:      &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", p.vCenterInternals.datastore.Name())},
		NumCPUs:    int32(vm.vCpus),
		MemoryMB:   int64(vm.mem),
		Annotation: groupID + "\n" + vm.annotation,
	}

	scsi, err := object.SCSIControllerTypes().CreateSCSIController("pvscsi")
	if err != nil {
		return errors.New("Error creating pvscsi controller as part of new VM")
	}

	spec.DeviceChange = append(spec.DeviceChange, &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationAdd,
		Device:    scsi,
	})

	// Create a new finder that will discover the defaults and are looked for Networks/Datastores
	f := find.NewFinder(p.vCenterInternals.client.Client, true)

	// Find one and only datacenter, not sure how VMware linked mode will work
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		log.Fatalf("No Datacenter instance could be found inside of vCenter %v", err)
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)

	groupFolder, err := f.Folder(ctx, "infrakit/"+groupID)
	if err != nil {
		log.Warnf("%v", err)
		groupFolder, err = p.vCenterInternals.dcFolders.VmFolder.CreateFolder(ctx, "infrakit/"+groupID)
		if err != nil {
			if err.Error() == "ServerFaultCode: The operation is not supported on the object." {
				baseFolder, _ := dc.Folders(ctx)
				groupFolder = baseFolder.VmFolder
			} else {
				log.Warnf("%v", err)
			}
		}
	}

	task, err := groupFolder.CreateVM(ctx, spec, p.vCenterInternals.resourcePool, p.vCenterInternals.hostSystem)
	if err != nil {
		return errors.New("Creating new VM failed, more detail can be found in vCenter tasks")
	}

	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return errors.New(fmt.Sprintf("Creating new VM failed\n%v", err))
	}

	// Retrieve the new VM
	newVM := object.NewVirtualMachine(p.vCenterInternals.client.Client, info.Result.(types.ManagedObjectReference))

	addISO(p, *vm, newVM)

	if p.vCenterInternals.network != nil {
		findNetwork(p.vC, p.vCenterInternals)
		addNIC(newVM, p.vCenterInternals.network)
	}

	if vm.persistentSz > 0 {
		addVMDK(p, newVM, *vm)
	}

	if vm.poweron == true {
		log.Infoln("Powering on LinuxKit VM")
		return powerOnVM(newVM)
	}
	return nil
}

func deleteVM(p *plugin, vm string) error {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Create a new finder that will discover the defaults and are looked for Networks/Datastores
	f := find.NewFinder(p.vCenterInternals.client.Client, true)

	// Find one and only datacenter, not sure how VMware linked mode will work
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		return errors.New(fmt.Sprintf("No Datacenter instance could be found inside of vCenter %v", err))
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)
	foundVM, err := f.VirtualMachine(ctx, vm)

	task, err := foundVM.Destroy(ctx)
	if err != nil {
		return errors.New("Delete operation has failed, more detail can be found in vCenter tasks")
	}

	_, err = task.WaitForResult(ctx, nil)
	if err != nil {
		return errors.New("Delete Task has failed, more detail can be found in vCenter tasks")
	}
	return nil
}

func powerOnVM(vm *object.VirtualMachine) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task, err := vm.PowerOn(ctx)
	if err != nil {
		return errors.New("Power On operation has failed, more detail can be found in vCenter tasks")
	}

	_, err = task.WaitForResult(ctx, nil)
	if err != nil {
		return errors.New("Power On Task has failed, more detail can be found in vCenter tasks")
	}
	return nil
}

func addNIC(vm *object.VirtualMachine, net object.NetworkReference) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backing, err := net.EthernetCardBackingInfo(ctx)
	if err != nil {
		log.Fatalf("Unable to determine vCenter network backend\n%v", err)
	}

	netdev, err := object.EthernetCardTypes().CreateEthernetCard("vmxnet3", backing)
	if err != nil {
		log.Fatalf("Unable to create vmxnet3 network interface\n%v", err)
	}

	log.Infof("Adding VM Networking")
	var add []types.BaseVirtualDevice
	add = append(add, netdev)

	if vm.AddDevice(ctx, add...); err != nil {
		log.Fatalf("Unable to add new networking device to VM configuration\n%v", err)
	}
}

func addVMDK(p *plugin, vm *object.VirtualMachine, newVM vmInstance) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices, err := vm.Device(ctx)
	if err != nil {
		log.Fatalf("Unable to read devices from VM configuration\n%v", err)
	}

	controller, err := devices.FindDiskController("scsi")
	if err != nil {
		log.Fatalf("Unable to find SCSI device from VM configuration\n%v", err)
	}
	// The default is to have all persistent disks named linuxkit.vmdk
	disk := devices.CreateDisk(controller, p.vCenterInternals.datastore.Reference(), p.vCenterInternals.datastore.Path(fmt.Sprintf("%s/%s", newVM.vmName, newVM.vmName+".vmdk")))

	disk.CapacityInKB = int64(newVM.persistentSz * 1024)

	var add []types.BaseVirtualDevice
	add = append(add, disk)

	log.Infof("Adding a persistent disk to the Virtual Machine")

	if vm.AddDevice(ctx, add...); err != nil {
		log.Fatalf("Unable to add new storage device to VM configuration\n%v", err)
	}
}

func addISO(p *plugin, newInstance vmInstance, vm *object.VirtualMachine) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devices, err := vm.Device(ctx)
	if err != nil {
		log.Fatalf("Unable to read devices from VM configuration\n%v", err)
	}

	ide, err := devices.FindIDEController("")
	if err != nil {
		log.Fatalf("Unable to find IDE device from VM configuration\n%v", err)
	}

	cdrom, err := devices.CreateCdrom(ide)
	if err != nil {
		log.Fatalf("Unable to create new CDROM device\n%v", err)
	}

	var add []types.BaseVirtualDevice
	add = append(add, devices.InsertIso(cdrom, p.vCenterInternals.datastore.Path(fmt.Sprintf("%s", newInstance.isoPath))))

	log.Infof("Adding ISO to the Virtual Machine")

	if vm.AddDevice(ctx, add...); err != nil {
		log.Fatalf("Unable to add new CD-ROM device to VM configuration\n%v", err)
	}
}
