#!/usr/bin/env bash

# https://vaneyckt.io/posts/safer_bash_scripts_with_set_euxo_pipefail/
set -Eeuxo pipefail

echo "Running the ${0} setup"

# `oc new-project` records the new project as the current context. A local
# kubeconfig is updated in place. When that file is read-only (a CI Secret
# mount, or a local file the user cannot write), run against a copy so the
# context update still succeeds. KUBECONFIG may list several files; oc writes
# the first one and still reads the rest.
writable_kubeconfig=""
kubeconfig_spec="${KUBECONFIG:-${HOME}/.kube/config}"
kubeconfig_primary="${kubeconfig_spec%%:*}"
if [[ -e "${kubeconfig_primary}" && ! -w "${kubeconfig_primary}" ]]; then
    writable_kubeconfig="$(mktemp)"
    cp "${kubeconfig_primary}" "${writable_kubeconfig}"
    chmod 600 "${writable_kubeconfig}"
    if [[ "${kubeconfig_spec}" == *:* ]]; then
        export KUBECONFIG="${writable_kubeconfig}:${kubeconfig_spec#*:}"
    else
        export KUBECONFIG="${writable_kubeconfig}"
    fi
fi

TEST_NAMESPACE="odh-notebook-controller-system"

# Following variables are optional - if not set, the default values in relevant params.env
# will be used for both images. As such, if you want to run tests against your custom changes,
# be sure to perform a docker build and set these variables accordingly!
ODH_NOTEBOOK_CONTROLLER_IMAGE="${ODH_NOTEBOOK_CONTROLLER_IMAGE:-}"
KF_NOTEBOOK_CONTROLLER="${KF_NOTEBOOK_CONTROLLER:-}"


if test -n "${ODH_NOTEBOOK_CONTROLLER_IMAGE}"; then
    IFS=':' read -r -a CTRL_IMG <<< "${ODH_NOTEBOOK_CONTROLLER_IMAGE}"
    export IMG="${CTRL_IMG[0]}"
    export TAG="${CTRL_IMG[1]}"
fi

if test -n "${KF_NOTEBOOK_CONTROLLER}"; then
    IFS=':' read -r -a KF_NBC_IMG <<< "${KF_NOTEBOOK_CONTROLLER}"
    export KF_IMG="${KF_NBC_IMG[0]}"
    export KF_TAG="${KF_NBC_IMG[1]}"
fi

export K8S_NAMESPACE="${TEST_NAMESPACE}"

# From now on we want to be sure that undeploy and testing project deletion are called

function cleanup() {
    local ret_code=0

    echo "Performing deployment cleanup of the ${0}"
    make undeploy || {
        echo "Warning [cleanup]: make undeploy failed, continuing with project deletion!"
        ret_code=1
    }
    oc delete --wait=true --ignore-not-found=true project "${TEST_NAMESPACE}" || {
        echo "Warning [cleanup]: project deletion failed!"
        ret_code=1
    }
    if [[ -n "${writable_kubeconfig}" ]]; then
        rm -f "${writable_kubeconfig}"
    fi
    return ${ret_code}
}
trap cleanup EXIT

# assure that the project is deleted on the cluster before running the tests
# Note: We only delete the project here, not calling cleanup() to avoid unnecessary make undeploy
oc delete --wait=true --ignore-not-found=true project "${TEST_NAMESPACE}" || echo "Warning [pre-test-cleanup]: project deletion failed!"

# setup and deploy the controller
oc new-project "${TEST_NAMESPACE}"

# deploy and run e2e tests
make deploy
make e2e-test
