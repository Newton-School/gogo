AGENTFLOW_PYTHON ?= python3
AGENTFLOW_PROPOSAL ?= $(PROPOSAL)

.PHONY: agentflow-help agentflow

agentflow-help:
	@printf '%s\n' \
		'AgentFlow project commands' \
		'' \
		'  make agentflow [PROPOSAL=proposal-id]  Generate architecture documentation'

agentflow:
	@$(AGENTFLOW_PYTHON) .agentflow/scripts/agentflow.py render $(if $(AGENTFLOW_PROPOSAL),--proposal "$(AGENTFLOW_PROPOSAL)",) --comments
