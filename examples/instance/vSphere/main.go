package main

import (
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/infrakit/pkg/cli"
	instance_plugin "github.com/docker/infrakit/pkg/rpc/instance"
	"github.com/spf13/cobra"
)

func main() {

	cmd := &cobra.Command{
		Use:   os.Args[0],
		Short: "VMware vSphere instance plugin",
	}

	var newVMInstance vmInstance
	var newVCenter vCenter

	name := cmd.Flags().String("name", "instance-vSphere", "Plugin name to advertise for discovery")
	logLevel := cmd.Flags().Int("log", cli.DefaultLogLevel, "Logging level. 0 is least verbose. Max is 5")
	//	dir := cmd.Flags().String("dir", os.TempDir(), "Dir for storing the files")

	// Attributes of the VMware vCenter Server to connect to
	newVCenter.vCenterURL = cmd.Flags().String("url", os.Getenv("VCURL"), "URL of VMware vCenter in the format of https://username:password@VCaddress/sdk")
	newVCenter.dsName = cmd.Flags().String("datastore", os.Getenv("VCDATASTORE"), "The name of the DataStore to host the VM")
	newVCenter.networkName = cmd.Flags().String("network", os.Getenv("VCNETWORK"), "The network label the VM will use")
	newVCenter.vSphereHost = cmd.Flags().String("hostname", os.Getenv("VCHOST"), "The server that will run the VM")

	// Attributes that will be applied to every new instance
	newVMInstance.isoPath = cmd.Flags().String("isopath", "", "The path on the datastore to the location of an ISO to be used. \"e.g. folder/file.iso\"")

	newVMInstance.vmTemplate = cmd.Flags().String("template", "", "The name of a template on the datastore to use [UNUSED]")

	newVMInstance.persistent = cmd.Flags().String("persistentSize", "", "Size in MB of persistent storage to allocate to the VM")
	newVMInstance.mem = cmd.Flags().Int64("mem", 1024, "Size in MB of memory to allocate to the VM")
	newVMInstance.vCpus = cmd.Flags().Int("cpus", 1, "Amount of vCPUs to allocate to the VM")
	newVMInstance.poweron = cmd.Flags().Bool("powerOn", false, "Power On the new VM once it has been created")
	newVMInstance.vmPrefix = cmd.Flags().String("vmPrefix", "vm-", "A prefix for created virtual machines. e.g. vm-{UUID}")

	cmd.Run = func(c *cobra.Command, args []string) {
		cli.SetLogLevel(*logLevel)
		cli.RunPlugin(*name, instance_plugin.PluginServer(NewVSphereInstancePlugin(&newVMInstance, &newVCenter)))
	}

	cmd.AddCommand(cli.VersionCommand())

	err := cmd.Execute()
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
}
