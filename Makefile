# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

.PHONY: all help test test-intro test-gemini-platform

all: test

help:
	@echo "Agent Substrate Workshop"
	@echo ""
	@echo "Available targets:"
	@echo "  make test                 Run test suites for all workshops (intro and gemini-platform)"
	@echo "  make test-intro           Run test suite for Intro workshop"
	@echo "  make test-gemini-platform Run test suite for Gemini platform workshop"
	@echo ""
	@echo "To work in a specific workshop, navigate to its directory:"
	@echo "  cd intro && make help"
	@echo "  cd gemini-platform && make help"

test: test-intro test-gemini-platform

test-intro:
	$(MAKE) -C intro test

test-gemini-platform:
	$(MAKE) -C gemini-platform test
