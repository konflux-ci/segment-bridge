#!/bin/bash
# splunk-to-segment.sh
#   Adapt user journey records loaded from Splunk for uploading into Segment:
#   - Convert nested JSON objects from strings to actual objects.
#   - Combine the event_* fields into a single UI-flavoured event string.
#   Uses .userId or .namespace from each record as the Segment userId.
#
set -o pipefail -o errexit -o nounset



function event_verb_map() {
  # Print a JSON map for converting preset tense K8s API server verbs to past
  # tense
  echo '{
    "create": "created",
    "delete": "deleted",
    "deletecollection": "collection deleted",
    "get": "fetched",
    "head": "headers fetched",
    "list": "listed",
    "patch": "patched",
    "update": "updated",
    "watch": "watch started"
  }'
}

function event_subject_map() {
  # Print a JSON map for converting the plural resource names found in the
  # audit log to the singular, capitalized names users are used to seeing
  echo '{
    "applications": "Application",
    "bannedusers": "BannedUser",
    "buildpipelineselectors": "BuildPipelineSelector",
    "componentdetectionqueries": "ComponentDetectionQuery",
    "components": "Component",
    "customruns": "CustomRun",
    "deploymenttargetclaims": "DeploymentTargetClaim",
    "deploymenttargets": "DeploymentTarget",
    "enterprisecontractpolicies": "EnterpriseContractPolicy",
    "environments": "Environment",
    "integrationtestscenarios": "IntegrationTestScenario",
    "internalrequests": "InternalRequest",
    "masteruserrecords": "MasterUserRecord",
    "memberoperatorconfigs": "MemberOperatorConfig",
    "memberstatuses": "MemberStatus",
    "notifications": "Notification",
    "nstemplatesets": "NSTemplateSet",
    "nstemplatetiers": "NSTemplateTier",
    "pipelineresources": "PipelineResource",
    "pipelineruns": "PipelineRun",
    "pipelines": "Pipeline",
    "promotionruns": "PromotionRun",
    "proxyplugins": "ProxyPlugin",
    "releaseplanadmissions": "ReleasePlanAdmission",
    "releaseplans": "ReleasePlan",
    "releases": "Release",
    "releasestrategies": "ReleaseStrategy",
    "remotesecrets": "RemoteSecret",
    "runs": "Run",
    "snapshotenvironmentbindings": "SnapshotEnvironmentBinding",
    "snapshots": "Snapshot",
    "socialevents": "SocialEvent",
    "spacebindings": "SpaceBinding",
    "spacerequests": "SpaceRequest",
    "spaces": "Space",
    "spiaccesschecks": "SPIAccessCheck",
    "spiaccesstokenbindings": "SPIAccessTokenBinding",
    "spiaccesstokendataupdates": "SPIAccessTokenDataUpdate",
    "spiaccesstokens": "SPIAccessToken",
    "spifilecontentrequests": "SPIFileContentRequest",
    "taskruns": "TaskRun",
    "tasks": "Task",
    "tiertemplates": "TierTemplate",
    "toolchainclusters": "ToolChainCluster",
    "toolchainconfigs": "ToolChainConfig",
    "toolchainstatuses": "ToolChainStatus",
    "useraccounts": "UserAccount",
    "usersignups": "UserSignup",
    "usertiers": "UserTier",
    "verificationpolicies": "VerificationPolicy"
  }'
}

# Emit one Segment event per result record. userId is taken from .userId or .namespace.
jq \
  --compact-output \
  --slurpfile evvm <(event_verb_map) \
  --slurpfile evsm <(event_subject_map) \
  'select(.result)
  | .result
  | (.userId // .namespace) as $userId
  | select($userId)
  | {
      messageId,
      timestamp,
      namespace,
      type,
      userId: $userId,
      event: (.event // "\($evsm[0][.event_subject] // .event_subject) \($evvm[0][.event_verb] // .event_verb)"),
      properties: (.properties|fromjson|.workspaceID=$userId),
      context: (.context|fromjson)
    }
  '
