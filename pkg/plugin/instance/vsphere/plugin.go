package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/docker/infrakit/pkg/spi"
	"github.com/docker/infrakit/pkg/spi/instance"
	"github.com/docker/infrakit/pkg/types"

	log "github.com/Sirupsen/logrus"
)

// This example uses local files as a representation of an instance.  When we
// create an instance, we write a file in a directory.  The content of the file is simply
// the message in the provision spec, so we can verify correctness of the content easily.
// When we destroy an instance, we remove the file.
// DescribeInstances simply would list the files with the matching
// tags.

// Spec is just whatever that can be unmarshalled into a generic JSON map
type Spec map[string]interface{}

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

type plugin struct {
	ctx              context.Context
	vC               *vCenter
	vCenterInternals *vcInternal
}

// NewVSphereInstancePlugin will take the cmdline/env configuration
func NewVSphereInstancePlugin(vc *vCenter) instance.Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Attempt to log in to VMware vCenter and return the internal variables needed
	internals, err := vCenterConnect(vc)
	if err != nil {
		fmt.Printf("%v", err)
	}
	return &plugin{
		ctx:              ctx,
		vC:               vc,
		vCenterInternals: &internals,
	}
}

// Info returns a vendor specific name and version
func (p *plugin) VendorInfo() *spi.VendorInfo {
	return &spi.VendorInfo{
		InterfaceSpec: spi.InterfaceSpec{
			Name:    "infrakit-instance-vSphere",
			Version: "0.6.0",
		},
		URL: "https://github.com/docker/infrakit",
	}
}

// ExampleProperties returns the properties / config of this plugin
func (p *plugin) ExampleProperties() *types.Any {
	any, err := types.AnyValue(Spec{
		"exampleString": "a_string",
		"exampleBool":   true,
		"exampleInt":    1,
	})
	if err != nil {
		return nil
	}
	return any
}

// Validate performs local validation on a provision request.
func (p *plugin) Validate(req *types.Any) error {
	log.Debugln("validate", req.String())

	spec := Spec{}
	if err := req.Decode(&spec); err != nil {
		return err
	}

	log.Debugln("Validated:", spec)
	return nil
}

// Provision creates a new instance based on the spec.
func (p *plugin) Provision(spec instance.Spec) (*instance.ID, error) {

	var properties map[string]interface{}

	if spec.Properties != nil {
		if err := spec.Properties.Decode(&properties); err != nil {
			return nil, fmt.Errorf("Invalid instance properties: %s\n", err)
		}
	}

	newInstance, err := parseParameters(properties, p)
	if err != nil {
		log.Errorf("Error: \n%v", err)
		return nil, err
	}

	err = setInternalStructures(p.vC, p.vCenterInternals)
	if err != nil {
		log.Errorf("Error: \n%v", err)
		return nil, err
	}

	if *p.vC.networkName != "" {
		findNetwork(p.vC, p.vCenterInternals)
	}

	// Use the VMware plugin data in order to provision a new VM server
	vmName := instance.ID(fmt.Sprintf(newInstance.vmPrefix+"-%d", rand.Int63()))
	if spec.Tags != nil {
		log.Infof("Adding %s to Group %v", string(vmName), spec.Tags["infrakit.group"])
	}

	if err != nil {
		return nil, err
	}
	newInstance.vmName = string(vmName)
	createNewVMInstance(p, &newInstance, spec.Tags["infrakit.group"])
	return &vmName, nil
}

// Label labels the instance
func (p *plugin) Label(instance instance.ID, labels map[string]string) error {

	// fp := filepath.Join(p.Dir, string(instance))
	// buff, err := afero.ReadFile(p.fs, fp)
	// if err != nil {
	// 	return err
	// }
	// instanceData := fileInstance{}
	// err = json.Unmarshal(buff, &instanceData)
	// if err != nil {
	// 	return err
	// }

	// if instanceData.Description.Tags == nil {
	// 	instanceData.Description.Tags = map[string]string{}
	// }
	// for k, v := range labels {
	// 	instanceData.Description.Tags[k] = v
	// }

	// buff, err = json.MarshalIndent(instanceData, "", "")
	// log.Debugln("label:", instance, "data=", string(buff), "err=", err)
	// if err != nil {
	// 	return err
	// }
	return nil
}

// Destroy terminates an existing instance.
func (p *plugin) Destroy(instance instance.ID, context instance.Context) error {
	// fp := filepath.Join(p.Dir, string(instance))
	log.Debugln("destroy")
	fmt.Printf("%v\n %v \n", instance, context)
	deleteVM(p, string(instance))
	return nil
}

// DescribeInstances returns descriptions of all instances matching all of the provided tags.
// TODO - need to define the fitlering of tags => AND or OR of matches?
func (p *plugin) DescribeInstances(tags map[string]string, properties bool) ([]instance.Description, error) {
	log.Debugln("describe-instances", tags)
	groupName := tags["infrakit.group"]
	instances, err := findGroupInstances(p, groupName)
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		log.Warnln("No Instances found")
	}

	results := []instance.Description{}

	for _, vmInstance := range instances {
		lid := (instance.LogicalID)(vmInstance.Name())
		results = append(results, instance.Description{
			ID:        instance.ID(vmInstance.Name()),
			LogicalID: &lid,
			Tags:      tags,
		})

		// description.LogicalID = vmInstance.Name()
		// if len(tags) == 0 {
		// 	result = append(result, description)
		// } else {
		// 	for k, v := range tags {
		// 	}
		// 	result = append(result, description)
		// }
		//log.Warnf("Found %s", vmInstance.Name())
	}

	return results, nil
}
