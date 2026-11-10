package apis_test

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	aclv1 "github.com/datakaveri/dx-common-go/apis/acl/v1"
	cataloguev1 "github.com/datakaveri/dx-common-go/apis/catalogue/v1"
	userv1 "github.com/datakaveri/dx-common-go/apis/user/v1"
)

// TestContractIdentity pins what both servers (Java controlplane and the Go
// service) and every caller depend on: service names, RPC names and the Java
// generation options. A change here is a change to a contract two languages
// implement, so it must be deliberate and copied to the Java dx-common too.
func TestContractIdentity(t *testing.T) {
	cases := []struct {
		file        protoreflect.FileDescriptor
		service     protoreflect.FullName
		rpcs        []string
		javaPackage string
	}{
		{cataloguev1.File_dx_apis_catalogue_v1_item_proto,
			"dx.apis.catalogue.v1.ItemService", []string{"GetItem"},
			"org.cdpg.dx.apis.catalogue.v1"},
		{userv1.File_dx_apis_user_v1_user_proto,
			"dx.apis.user.v1.UserService", []string{"GetUser", "GrantRoles", "RevokeRoles"},
			"org.cdpg.dx.apis.user.v1"},
		{aclv1.File_dx_apis_acl_v1_policy_proto,
			"dx.apis.acl.v1.PolicyService", []string{"ListActivePolicies", "CreatePolicy"},
			"org.cdpg.dx.apis.acl.v1"},
	}
	for _, c := range cases {
		t.Run(string(c.service), func(t *testing.T) {
			svcs := c.file.Services()
			if svcs.Len() != 1 || svcs.Get(0).FullName() != c.service {
				t.Fatalf("want exactly service %s in %s", c.service, c.file.Path())
			}
			var got []string
			for i := 0; i < svcs.Get(0).Methods().Len(); i++ {
				got = append(got, string(svcs.Get(0).Methods().Get(i).Name()))
			}
			if !slices.Equal(got, c.rpcs) {
				t.Errorf("rpcs = %v, want %v", got, c.rpcs)
			}
			opts, _ := c.file.Options().(*descriptorpb.FileOptions)
			if opts.GetJavaPackage() != c.javaPackage || !opts.GetJavaMultipleFiles() {
				t.Errorf("java options = (%q, multiple=%v), want (%q, true)",
					opts.GetJavaPackage(), opts.GetJavaMultipleFiles(), c.javaPackage)
			}
		})
	}
}
