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

.PHONY: all help test test-101 test-301

all: test

help:
	@echo "Agent Substrate Workshop (Multi-Track Repository)"
	@echo ""
	@echo "Available targets:"
	@echo "  make test       Run test suites for all tracks (101 and 301)"
	@echo "  make test-101   Run test suite for Track 101 (Fundamentals)"
	@echo "  make test-301   Run test suite for Track 301 (Enterprise Platform)"
	@echo ""
	@echo "To work in a specific track, navigate to its directory:"
	@echo "  cd 101 && make help"
	@echo "  cd 301 && make help"

test: test-101 test-301

test-101:
	$(MAKE) -C 101 test

test-301:
	$(MAKE) -C 301 test
