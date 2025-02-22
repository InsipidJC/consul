package agent

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/lib"
)

// aclCreateResponse is used to wrap the ACL ID
type aclBootstrapResponse struct {
	ID string
	structs.ACLToken
}

var aclDisabled = UnauthorizedError{Reason: "ACL support disabled"}

// checkACLDisabled will return a standard response if ACLs are disabled. This
// returns true if they are disabled and we should not continue.
func (s *HTTPHandlers) checkACLDisabled() bool {
	if s.agent.config.ACLsEnabled {
		return false
	}
	return true
}

// ACLBootstrap is used to perform a one-time ACL bootstrap operation on
// a cluster to get the first management token.
func (s *HTTPHandlers) ACLBootstrap(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	args := structs.DCSpecificRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	var out structs.ACLToken
	err := s.agent.RPC("ACL.BootstrapTokens", &args, &out)
	if err != nil {
		if strings.Contains(err.Error(), structs.ACLBootstrapNotAllowedErr.Error()) {
			return nil, acl.PermissionDeniedError{Cause: err.Error()}
		} else {
			return nil, err
		}
	}
	return &aclBootstrapResponse{ID: out.SecretID, ACLToken: out}, nil
}

func (s *HTTPHandlers) ACLReplicationStatus(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	// Note that we do not forward to the ACL DC here. This is a query for
	// any DC that's doing replication.
	args := structs.DCSpecificRequest{}
	s.parseSource(req, &args.Source)
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	// Make the request.
	var out structs.ACLReplicationStatus
	if err := s.agent.RPC("ACL.ReplicationStatus", &args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *HTTPHandlers) ACLPolicyList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var args structs.ACLPolicyListRequest
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLPolicyListResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.PolicyList", &args, &out); err != nil {
		return nil, err
	}

	// make sure we return an array and not nil
	if out.Policies == nil {
		out.Policies = make(structs.ACLPolicyListStubs, 0)
	}

	return out.Policies, nil
}

func (s *HTTPHandlers) ACLPolicyCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var fn func(resp http.ResponseWriter, req *http.Request, policyID string) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLPolicyReadByID

	case "PUT":
		fn = s.ACLPolicyWrite

	case "DELETE":
		fn = s.ACLPolicyDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	policyID, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/policy/")
	if err != nil {
		return nil, err
	}
	if policyID == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing policy ID"}
	}

	return fn(resp, req, policyID)
}

func (s *HTTPHandlers) ACLPolicyRead(resp http.ResponseWriter, req *http.Request, policyID, policyName string) (interface{}, error) {
	args := structs.ACLPolicyGetRequest{
		Datacenter: s.agent.config.Datacenter,
		PolicyID:   policyID,
		PolicyName: policyName,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLPolicyResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.PolicyRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Policy == nil {
		// TODO(rb): should this return a normal 404?
		return nil, acl.ErrNotFound
	}

	return out.Policy, nil
}

func (s *HTTPHandlers) ACLPolicyReadByName(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	policyName, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/policy/name/")
	if err != nil {
		return nil, err
	}
	if policyName == "" {
		return nil, BadRequestError{Reason: "Missing policy Name"}
	}

	return s.ACLPolicyRead(resp, req, "", policyName)
}

func (s *HTTPHandlers) ACLPolicyReadByID(resp http.ResponseWriter, req *http.Request, policyID string) (interface{}, error) {
	return s.ACLPolicyRead(resp, req, policyID, "")
}

func (s *HTTPHandlers) ACLPolicyCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	return s.aclPolicyWriteInternal(resp, req, "", true)
}

func (s *HTTPHandlers) ACLPolicyWrite(resp http.ResponseWriter, req *http.Request, policyID string) (interface{}, error) {
	return s.aclPolicyWriteInternal(resp, req, policyID, false)
}

func (s *HTTPHandlers) aclPolicyWriteInternal(_resp http.ResponseWriter, req *http.Request, policyID string, create bool) (interface{}, error) {
	args := structs.ACLPolicySetRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.Policy.EnterpriseMeta); err != nil {
		return nil, err
	}

	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.Policy)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Policy decoding failed: %v", err)}
	}

	args.Policy.Syntax = acl.SyntaxCurrent

	if create {
		if args.Policy.ID != "" {
			return nil, BadRequestError{Reason: "Cannot specify the ID when creating a new policy"}
		}
	} else {
		if args.Policy.ID != "" && args.Policy.ID != policyID {
			return nil, BadRequestError{Reason: "Policy ID in URL and payload do not match"}
		} else if args.Policy.ID == "" {
			args.Policy.ID = policyID
		}
	}

	var out structs.ACLPolicy
	if err := s.agent.RPC("ACL.PolicySet", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPHandlers) ACLPolicyDelete(resp http.ResponseWriter, req *http.Request, policyID string) (interface{}, error) {
	args := structs.ACLPolicyDeleteRequest{
		Datacenter: s.agent.config.Datacenter,
		PolicyID:   policyID,
	}
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	var ignored string
	if err := s.agent.RPC("ACL.PolicyDelete", args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}

func (s *HTTPHandlers) ACLTokenList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	args := &structs.ACLTokenListRequest{
		IncludeLocal:  true,
		IncludeGlobal: true,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	args.Policy = req.URL.Query().Get("policy")
	args.Role = req.URL.Query().Get("role")
	args.AuthMethod = req.URL.Query().Get("authmethod")
	if err := parseACLAuthMethodEnterpriseMeta(req, &args.ACLAuthMethodEnterpriseMeta); err != nil {
		return nil, err
	}

	var out structs.ACLTokenListResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenList", &args, &out); err != nil {
		return nil, err
	}

	return out.Tokens, nil
}

func (s *HTTPHandlers) ACLTokenCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var fn func(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLTokenGet

	case "PUT":
		fn = s.ACLTokenSet

	case "DELETE":
		fn = s.ACLTokenDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	tokenID, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/token/")
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(tokenID, "/clone") && req.Method == "PUT" {
		tokenID = tokenID[:len(tokenID)-6]
		fn = s.ACLTokenClone
	}
	if tokenID == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing token ID"}
	}

	return fn(resp, req, tokenID)
}

func (s *HTTPHandlers) ACLTokenSelf(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	args := structs.ACLTokenGetRequest{
		TokenIDType: structs.ACLTokenSecret,
	}

	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	// copy the token parameter to the ID
	args.TokenID = args.Token

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLTokenResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Token == nil {
		return nil, acl.ErrNotFound
	}

	return out.Token, nil
}

func (s *HTTPHandlers) ACLTokenCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	return s.aclTokenSetInternal(req, "", true)
}

func (s *HTTPHandlers) ACLTokenGet(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	args := structs.ACLTokenGetRequest{
		Datacenter:  s.agent.config.Datacenter,
		TokenID:     tokenID,
		TokenIDType: structs.ACLTokenAccessor,
	}

	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}
	if _, ok := req.URL.Query()["expanded"]; ok {
		args.Expanded = true
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLTokenResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Token == nil {
		return nil, acl.ErrNotFound
	}

	if args.Expanded {
		expanded := &structs.ACLTokenExpanded{
			ACLToken:          out.Token,
			ExpandedTokenInfo: out.ExpandedTokenInfo,
		}
		return expanded, nil
	}

	return out.Token, nil
}

func (s *HTTPHandlers) ACLTokenSet(_ http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	return s.aclTokenSetInternal(req, tokenID, false)
}

func (s *HTTPHandlers) aclTokenSetInternal(req *http.Request, tokenID string, create bool) (interface{}, error) {
	args := structs.ACLTokenSetRequest{
		Datacenter: s.agent.config.Datacenter,
		Create:     create,
	}
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.ACLToken.EnterpriseMeta); err != nil {
		return nil, err
	}

	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.ACLToken)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Token decoding failed: %v", err)}
	}

	if !create {
		if args.ACLToken.AccessorID != "" && args.ACLToken.AccessorID != tokenID {
			return nil, BadRequestError{Reason: "Token Accessor ID in URL and payload do not match"}
		} else if args.ACLToken.AccessorID == "" {
			args.ACLToken.AccessorID = tokenID
		}
	}

	var out structs.ACLToken
	if err := s.agent.RPC("ACL.TokenSet", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPHandlers) ACLTokenDelete(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	args := structs.ACLTokenDeleteRequest{
		Datacenter: s.agent.config.Datacenter,
		TokenID:    tokenID,
	}
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	var ignored string
	if err := s.agent.RPC("ACL.TokenDelete", args, &ignored); err != nil {
		return nil, err
	}
	return true, nil
}

func (s *HTTPHandlers) ACLTokenClone(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	args := structs.ACLTokenSetRequest{
		Datacenter: s.agent.config.Datacenter,
		Create:     true,
	}

	if err := s.parseEntMeta(req, &args.ACLToken.EnterpriseMeta); err != nil {
		return nil, err
	}
	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.ACLToken)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Token decoding failed: %v", err)}
	}
	s.parseToken(req, &args.Token)

	// Set this for the ID to clone
	args.ACLToken.AccessorID = tokenID

	var out structs.ACLToken
	if err := s.agent.RPC("ACL.TokenClone", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPHandlers) ACLRoleList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var args structs.ACLRoleListRequest
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	args.Policy = req.URL.Query().Get("policy")

	var out structs.ACLRoleListResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.RoleList", &args, &out); err != nil {
		return nil, err
	}

	// make sure we return an array and not nil
	if out.Roles == nil {
		out.Roles = make(structs.ACLRoles, 0)
	}

	return out.Roles, nil
}

func (s *HTTPHandlers) ACLRoleCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var fn func(resp http.ResponseWriter, req *http.Request, roleID string) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLRoleReadByID

	case "PUT":
		fn = s.ACLRoleWrite

	case "DELETE":
		fn = s.ACLRoleDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	roleID, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/role/")
	if err != nil {
		return nil, err
	}
	if roleID == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing role ID"}
	}

	return fn(resp, req, roleID)
}

func (s *HTTPHandlers) ACLRoleReadByName(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	roleName, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/role/name/")
	if err != nil {
		return nil, err
	}
	if roleName == "" {
		return nil, BadRequestError{Reason: "Missing role Name"}
	}

	return s.ACLRoleRead(resp, req, "", roleName)
}

func (s *HTTPHandlers) ACLRoleReadByID(resp http.ResponseWriter, req *http.Request, roleID string) (interface{}, error) {
	return s.ACLRoleRead(resp, req, roleID, "")
}

func (s *HTTPHandlers) ACLRoleRead(resp http.ResponseWriter, req *http.Request, roleID, roleName string) (interface{}, error) {
	args := structs.ACLRoleGetRequest{
		Datacenter: s.agent.config.Datacenter,
		RoleID:     roleID,
		RoleName:   roleName,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLRoleResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.RoleRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Role == nil {
		resp.WriteHeader(http.StatusNotFound)
		return nil, nil
	}

	return out.Role, nil
}

func (s *HTTPHandlers) ACLRoleCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	return s.ACLRoleWrite(resp, req, "")
}

func (s *HTTPHandlers) ACLRoleWrite(resp http.ResponseWriter, req *http.Request, roleID string) (interface{}, error) {
	args := structs.ACLRoleSetRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.Role.EnterpriseMeta); err != nil {
		return nil, err
	}

	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.Role)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Role decoding failed: %v", err)}
	}

	if args.Role.ID != "" && args.Role.ID != roleID {
		return nil, BadRequestError{Reason: "Role ID in URL and payload do not match"}
	} else if args.Role.ID == "" {
		args.Role.ID = roleID
	}

	var out structs.ACLRole
	if err := s.agent.RPC("ACL.RoleSet", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPHandlers) ACLRoleDelete(resp http.ResponseWriter, req *http.Request, roleID string) (interface{}, error) {
	args := structs.ACLRoleDeleteRequest{
		Datacenter: s.agent.config.Datacenter,
		RoleID:     roleID,
	}
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	var ignored string
	if err := s.agent.RPC("ACL.RoleDelete", args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}

func (s *HTTPHandlers) ACLBindingRuleList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var args structs.ACLBindingRuleListRequest
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	args.AuthMethod = req.URL.Query().Get("authmethod")

	var out structs.ACLBindingRuleListResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.BindingRuleList", &args, &out); err != nil {
		return nil, err
	}

	// make sure we return an array and not nil
	if out.BindingRules == nil {
		out.BindingRules = make(structs.ACLBindingRules, 0)
	}

	return out.BindingRules, nil
}

func (s *HTTPHandlers) ACLBindingRuleCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var fn func(resp http.ResponseWriter, req *http.Request, bindingRuleID string) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLBindingRuleRead

	case "PUT":
		fn = s.ACLBindingRuleWrite

	case "DELETE":
		fn = s.ACLBindingRuleDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	bindingRuleID, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/binding-rule/")
	if err != nil {
		return nil, err
	}
	if bindingRuleID == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing binding rule ID"}
	}

	return fn(resp, req, bindingRuleID)
}

func (s *HTTPHandlers) ACLBindingRuleRead(resp http.ResponseWriter, req *http.Request, bindingRuleID string) (interface{}, error) {
	args := structs.ACLBindingRuleGetRequest{
		Datacenter:    s.agent.config.Datacenter,
		BindingRuleID: bindingRuleID,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLBindingRuleResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.BindingRuleRead", &args, &out); err != nil {
		return nil, err
	}

	if out.BindingRule == nil {
		resp.WriteHeader(http.StatusNotFound)
		return nil, nil
	}

	return out.BindingRule, nil
}

func (s *HTTPHandlers) ACLBindingRuleCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	return s.ACLBindingRuleWrite(resp, req, "")
}

func (s *HTTPHandlers) ACLBindingRuleWrite(resp http.ResponseWriter, req *http.Request, bindingRuleID string) (interface{}, error) {
	args := structs.ACLBindingRuleSetRequest{}
	s.parseDC(req, &args.Datacenter)
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.BindingRule.EnterpriseMeta); err != nil {
		return nil, err
	}

	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.BindingRule)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("BindingRule decoding failed: %v", err)}
	}

	if args.BindingRule.ID != "" && args.BindingRule.ID != bindingRuleID {
		return nil, BadRequestError{Reason: "BindingRule ID in URL and payload do not match"}
	} else if args.BindingRule.ID == "" {
		args.BindingRule.ID = bindingRuleID
	}

	var out structs.ACLBindingRule
	if err := s.agent.RPC("ACL.BindingRuleSet", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPHandlers) ACLBindingRuleDelete(resp http.ResponseWriter, req *http.Request, bindingRuleID string) (interface{}, error) {
	args := structs.ACLBindingRuleDeleteRequest{
		BindingRuleID: bindingRuleID,
	}
	s.parseDC(req, &args.Datacenter)
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	var ignored bool
	if err := s.agent.RPC("ACL.BindingRuleDelete", args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}

func (s *HTTPHandlers) ACLAuthMethodList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var args structs.ACLAuthMethodListRequest
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLAuthMethodListResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.AuthMethodList", &args, &out); err != nil {
		return nil, err
	}

	// make sure we return an array and not nil
	if out.AuthMethods == nil {
		out.AuthMethods = make(structs.ACLAuthMethodListStubs, 0)
	}

	return out.AuthMethods, nil
}

func (s *HTTPHandlers) ACLAuthMethodCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	var fn func(resp http.ResponseWriter, req *http.Request, methodName string) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLAuthMethodRead

	case "PUT":
		fn = s.ACLAuthMethodWrite

	case "DELETE":
		fn = s.ACLAuthMethodDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	methodName, err := getPathSuffixUnescaped(req.URL.Path, "/v1/acl/auth-method/")
	if err != nil {
		return nil, err
	}
	if methodName == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing auth method name"}
	}

	return fn(resp, req, methodName)
}

func (s *HTTPHandlers) ACLAuthMethodRead(resp http.ResponseWriter, req *http.Request, methodName string) (interface{}, error) {
	args := structs.ACLAuthMethodGetRequest{
		Datacenter:     s.agent.config.Datacenter,
		AuthMethodName: methodName,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLAuthMethodResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.AuthMethodRead", &args, &out); err != nil {
		return nil, err
	}

	if out.AuthMethod == nil {
		resp.WriteHeader(http.StatusNotFound)
		return nil, nil
	}

	fixupAuthMethodConfig(out.AuthMethod)
	return out.AuthMethod, nil
}

func (s *HTTPHandlers) ACLAuthMethodCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	return s.ACLAuthMethodWrite(resp, req, "")
}

func (s *HTTPHandlers) ACLAuthMethodWrite(resp http.ResponseWriter, req *http.Request, methodName string) (interface{}, error) {
	args := structs.ACLAuthMethodSetRequest{}
	s.parseDC(req, &args.Datacenter)
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.AuthMethod.EnterpriseMeta); err != nil {
		return nil, err
	}

	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.AuthMethod)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("AuthMethod decoding failed: %v", err)}
	}

	if methodName != "" {
		if args.AuthMethod.Name != "" && args.AuthMethod.Name != methodName {
			return nil, BadRequestError{Reason: "AuthMethod Name in URL and payload do not match"}
		} else if args.AuthMethod.Name == "" {
			args.AuthMethod.Name = methodName
		}
	}

	var out structs.ACLAuthMethod
	if err := s.agent.RPC("ACL.AuthMethodSet", args, &out); err != nil {
		return nil, err
	}

	fixupAuthMethodConfig(&out)
	return &out, nil
}

func (s *HTTPHandlers) ACLAuthMethodDelete(resp http.ResponseWriter, req *http.Request, methodName string) (interface{}, error) {
	args := structs.ACLAuthMethodDeleteRequest{
		AuthMethodName: methodName,
	}
	s.parseDC(req, &args.Datacenter)
	s.parseToken(req, &args.Token)
	if err := s.parseEntMeta(req, &args.EnterpriseMeta); err != nil {
		return nil, err
	}

	var ignored bool
	if err := s.agent.RPC("ACL.AuthMethodDelete", args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}

func (s *HTTPHandlers) ACLLogin(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	args := &structs.ACLLoginRequest{
		Datacenter: s.agent.config.Datacenter,
		Auth:       &structs.ACLLoginParams{},
	}
	s.parseDC(req, &args.Datacenter)
	if err := s.parseEntMeta(req, &args.Auth.EnterpriseMeta); err != nil {
		return nil, err
	}

	if err := s.rewordUnknownEnterpriseFieldError(lib.DecodeJSON(req.Body, &args.Auth)); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Failed to decode request body: %v", err)}
	}

	var out structs.ACLToken
	if err := s.agent.RPC("ACL.Login", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPHandlers) ACLLogout(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	args := structs.ACLLogoutRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	s.parseDC(req, &args.Datacenter)
	s.parseToken(req, &args.Token)

	if args.Token == "" {
		return nil, acl.ErrNotFound
	}

	var ignored bool
	if err := s.agent.RPC("ACL.Logout", &args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}

// A hack to fix up the config types inside of the map[string]interface{}
// so that they get formatted correctly during json.Marshal. Without this,
// string values that get converted to []uint8 end up getting output back
// to the user in base64-encoded form.
func fixupAuthMethodConfig(method *structs.ACLAuthMethod) {
	for k, v := range method.Config {
		if raw, ok := v.([]uint8); ok {
			strVal := structs.Uint8ToString(raw)
			method.Config[k] = strVal
		}
	}
}

func (s *HTTPHandlers) ACLAuthorize(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	// At first glance it may appear like this endpoint is going to leak security relevant information.
	// There are a number of reason why this is okay.
	//
	// 1. The authorizations performed here are the same as what would be done if other HTTP APIs
	//    were used. This is just a way to see if it would be allowed. In the future when we have
	//    audit logging, these authorization checks will be logged along with those from the real
	//    endpoints. In that respect, you can figure out if you have access just as easily by
	//    attempting to perform the requested operation.
	// 2. In order to use this API you must have a valid ACL token secret.
	// 3. Along with #2 you can use the ACL.GetPolicy RPC endpoint which will return a rolled up
	//    set of policy rules showing your tokens effective policy. This RPC endpoint exposes
	//    more information than this one and has been around since before v1.0.0. With that other
	//    endpoint you get to see all things possible rather than having to have a list of things
	//    you may want to do and to request authorizations for each one.
	// 4. In addition to the legacy ACL.GetPolicy RPC endpoint we have an ACL.PolicyResolve and
	//    ACL.RoleResolve endpoints. These RPC endpoints allow reading roles and policies so long
	//    as the token used for the request is linked with them. This is needed to allow client
	//    agents to pull the policy and roles for a token that they are resolving. The only
	//    alternative to this style of access would be to make every agent use a token
	//    with acl:read privileges for all policy and role resolution requests. Once you have
	//    all the associated policies and roles it would be easy enough to recreate the effective
	//    policy.
	const maxRequests = 64

	if s.checkACLDisabled() {
		return nil, aclDisabled
	}

	request := structs.RemoteACLAuthorizationRequest{
		Datacenter: s.agent.config.Datacenter,
		QueryOptions: structs.QueryOptions{
			AllowStale:        true,
			RequireConsistent: false,
		},
	}
	var responses []structs.ACLAuthorizationResponse

	s.parseToken(req, &request.Token)
	s.parseDC(req, &request.Datacenter)

	if err := decodeBody(req.Body, &request.Requests); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Failed to decode request body: %v", err)}
	}

	if len(request.Requests) > maxRequests {
		return nil, BadRequestError{Reason: fmt.Sprintf("Refusing to process more than %d authorizations at once", maxRequests)}
	}

	if len(request.Requests) == 0 {
		return make([]structs.ACLAuthorizationResponse, 0), nil
	}

	if request.Datacenter != "" && request.Datacenter != s.agent.config.Datacenter {
		// when we are targeting a datacenter other than our own then we must issue an RPC
		// to perform the resolution as it may involve a local token
		if err := s.agent.RPC("ACL.Authorize", &request, &responses); err != nil {
			return nil, err
		}
	} else {
		authz, err := s.agent.delegate.ResolveTokenAndDefaultMeta(request.Token, nil, nil)
		if err != nil {
			return nil, err
		}

		responses, err = structs.CreateACLAuthorizationResponses(authz, request.Requests)
		if err != nil {
			return nil, BadRequestError{Reason: err.Error()}
		}
	}

	if responses == nil {
		responses = make([]structs.ACLAuthorizationResponse, 0)
	}

	return responses, nil
}
