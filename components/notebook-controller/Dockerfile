# Build arguments
ARG SOURCE_CODE=.
ARG CI_CONTAINER_VERSION="unknown"

# Use ubi8/go-toolset as base image
FROM registry.access.redhat.com/ubi8/go-toolset:1.20 as builder

## Build args to be used at this step
ARG SOURCE_CODE

# Set building workdir
WORKDIR /opt/rhods

# Copy the Go Modules manifests
COPY ${SOURCE_CODE}/notebook-controller ./notebook-controller
COPY ${SOURCE_CODE}/common ./common

# Update building workdir
WORKDIR /opt/rhods/notebook-controller

## Build the kf-notebook-controller
USER root

RUN if [ -z ${CACHITO_ENV_FILE} ]; then \
       go mod download; \
    else \
       source ${CACHITO_ENV_FILE}; \
    fi

RUN go build \
        -o ./bin/manager main.go

# Use ubi8/ubi-minimal as base image
FROM registry.access.redhat.com/ubi8/ubi-minimal:latest

## Build args to be used at this step
ARG CI_CONTAINER_VERSION

## Install additional packages
RUN microdnf install -y shadow-utils &&\
    microdnf clean all

## Create a non-root user with UID 1001
RUN useradd --uid 1001 --create-home --user-group --system rhods

## Set workdir directory to user home
WORKDIR /home/rhods

## Copy kf-notebook-controller-manager binary from builder stage
COPY --from=builder \
      /opt/rhods/notebook-controller/bin/manager \
      /manager

## Switch to a non-root user
USER rhods

ENTRYPOINT [ "/manager" ]
