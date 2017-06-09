package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	// "github.com/vmware/govmomi/vim25/soap"
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
	isoPath *string
	// Used with a VMware VM template
	vmTemplate *string

	// InfraKit vSphere instance settings
	vmPrefix     *string
	persistent   *string
	persistentSz int
	vCpus        *int
	mem          *int
	poweron      *bool
	guestIP      *bool
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
		log.Fatalf("No Datacenter instance could be found inside of vCenter %v", err)
	}

	// Make future calls local to this datacenter
	f.SetDatacenter(dc)

	// Find Datastore/Network
	internals.datastore, err = f.DatastoreOrDefault(ctx, *vc.dsName)
	if err != nil {
		log.Fatalf("Datastore [%s], could not be found", *vc.dsName)
	}

	internals.dcFolders, err = dc.Folders(ctx)
	if err != nil {
		log.Fatalln("Error locating default datacenter folder")
	}

	// Set the host that the VM will be created on
	internals.hostSystem, err = f.HostSystemOrDefault(ctx, *vc.vSphereHost)
	if err != nil {
		log.Fatalf("vSphere host [%s], could not be found", *vc.vSphereHost)
	}

	// Find the resource pool attached to this host
	internals.resourcePool, err = internals.hostSystem.ResourcePool(ctx)
	if err != nil {
		log.Fatalln("Error locating default resource pool")
	}
	return nil
}

func parseParameters(properties map[string]interface{}, p *plugin) error {
	log.Infoln("Building Params")
	if *p.vC.vCenterURL == "" {
		if properties["vCenterURL"] == nil {
			return errors.New("Environment variable VCURL or .yml vCenterURL must be set")
		} else {
			*p.vC.vCenterURL = properties["vCenterURL"].(string)
		}
	}

	if properties["DataStore"] == nil {
		return errors.New("Property 'DataStore' must be set")
	} else {
		*p.vC.dsName = properties["DataStore"].(string)
		log.Infof("DataStore set to %s", *p.vC.dsName)
	}

	if properties["Hostname"] == nil {
		return errors.New("Property 'Hostname' must be set")
	} else {
		*p.vC.vSphereHost = properties["Hostname"].(string)
	}

	if properties["isoPath"] == nil {
		//log.Warnf("The property 'Network' hasn't been set, no networks will be attached to VM")
	} else {
		*p.instance.isoPath = properties["isoPath"].(string)
	}

	if properties["Network"] == nil {
		log.Warnf("The property 'Network' hasn't been set, no networks will be attached to VM")
	} else {
		*p.vC.networkName = properties["Network"].(string)
	}

	if properties["CPUs"] == nil {
		*p.instance.vCpus = 2
	} else {
		*p.instance.vCpus = int(properties["CPUs"].(float64))

	}
	if properties["Memory"] == nil {
		*p.instance.mem = 512
	} else {
		*p.instance.mem = int(properties["Memory"].(float64))
	}
	return nil
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

func createNewVMInstance(vm vmInstance, internals vcInternal, vmName string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("in the create New VM function creating : %s\n", vmName)
	spec := types.VirtualMachineConfigSpec{
		Name:     vmName,
		GuestId:  "otherLinux64Guest",
		Files:    &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", internals.datastore.Name())},
		NumCPUs:  int32(*vm.vCpus),
		MemoryMB: int64(*vm.mem),
	}

	scsi, err := object.SCSIControllerTypes().CreateSCSIController("pvscsi")
	if err != nil {
		return errors.New("Error creating pvscsi controller as part of new VM")
	}

	spec.DeviceChange = append(spec.DeviceChange, &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationAdd,
		Device:    scsi,
	})

	task, err := internals.dcFolders.VmFolder.CreateVM(ctx, spec, internals.resourcePool, internals.hostSystem)
	if err != nil {
		return errors.New("Creating new VM failed, more detail can be found in vCenter tasks")
	}

	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return errors.New(fmt.Sprintf("Creating new VM failed\n%v", err))
	}

	// Retrieve the new VM
	newVM := object.NewVirtualMachine(internals.client.Client, info.Result.(types.ManagedObjectReference))

	if *vm.poweron == true {
		log.Infoln("Powering on LinuxKit VM")
		return powerOnVM(newVM)
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
