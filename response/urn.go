package response

// URN constants for the resource server namespace.
const (
	URNRsSuccess      = "urn:dx:rs:success"
	URNRsCreated      = "urn:dx:rs:created"
	URNRsNotFound     = "urn:dx:rs:ResourceNotFound"
	URNRsInvalidParam = "urn:dx:rs:InvalidParamValue"
	URNRsUnauthorized = "urn:dx:rs:Unauthorized"
	URNRsForbidden    = "urn:dx:rs:Forbidden"
	URNRsInternal     = "urn:dx:rs:InternalServerError"
	URNRsConflict     = "urn:dx:rs:ResourceAlreadyExists"
)

// URN constants for the auth/catalogue service namespace (as/auth-server).
const (
	URNAsSuccess      = "urn:dx:as:success"
	URNAsCreated      = "urn:dx:as:created"
	URNAsUnauthorized = "urn:dx:as:Unauthorized"
	URNAsForbidden    = "urn:dx:as:Forbidden"
	URNAsNotFound     = "urn:dx:as:ResourceNotFound"
	URNAsConflict     = "urn:dx:as:ResourceAlreadyExists"
	URNAsInternal     = "urn:dx:as:InternalServerError"
	URNAsInvalidParam = "urn:dx:as:InvalidParamValue"
	URNAsTokenExpired = "urn:dx:as:TokenExpired" //nolint:gosec // G101 false positive: a response URN constant, not a credential
)

// URN constants for the catalogue service namespace.
const (
	URNCatSuccess = "urn:dx:cat:success"
	URNCatCreated = "urn:dx:cat:created"
)

// URN constants for the ACL service namespace.
const (
	URNAclSuccess = "urn:dx:acl:success"
	URNAclCreated = "urn:dx:acl:created"
)

// URN constants for the authorization (PDP) service namespace.
const (
	URNAuthzSuccess = "urn:dx:authz:success"
	URNAuthzCreated = "urn:dx:authz:created"
)

// URN constants for the marketplace service namespace.
const (
	URNDmpSuccess = "urn:dx:dmp:success"
	URNDmpCreated = "urn:dx:dmp:created"
)

// URN constants for the community layer namespace.
const (
	URNCommunitySuccess   = "urn:dx:community:success"
	URNCommunityCreated   = "urn:dx:community:created"
	URNCommunityNotFound  = "urn:dx:community:ResourceNotFound"
	URNCommunityConflict  = "urn:dx:community:ResourceAlreadyExists"
	URNCommunityForbidden = "urn:dx:community:Forbidden"
	URNCommunityInternal  = "urn:dx:community:InternalServerError"
)

// URN constants for the files service namespace.
const (
	URNFilesSuccess   = "urn:dx:files:success"
	URNFilesCreated   = "urn:dx:files:created"
	URNFilesNotFound  = "urn:dx:files:ResourceNotFound"
	URNFilesConflict  = "urn:dx:files:ResourceAlreadyExists"
	URNFilesForbidden = "urn:dx:files:Forbidden"
	URNFilesInternal  = "urn:dx:files:InternalServerError"
)
