SHELL := /bin/bash

.DEFAULT_GOAL := help

# AgentFlow architecture review
-include .agentflow/agentflow.mk

.PHONY: help
help: agentflow-help
