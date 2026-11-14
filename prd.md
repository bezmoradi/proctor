# Proctor — Product Vision and Technical Specification

**Working project name:** Proctor  
**Tagline:** Verify behavior, not implementation.  
**Project type:** Open-source, language-independent behavioral verification and testing system  
**Initial implementation language:** Go  
**Initial specification format:** YAML  
**Initial application integration:** HTTP / REST APIs  
**Initial verification targets:** HTTP, PostgreSQL, Temporal, and application logs

---

# 1. Executive Summary

Proctor is an independent behavioral verification system designed to verify that software applications behave according to human-approved specifications.

Its primary purpose is to address a growing problem in AI-assisted software development: AI coding agents can produce code that appears correct, write tests that pass, and even modify existing tests to accommodate incorrect implementations.

Traditional application tests are often maintained alongside the implementation. When an AI agent changes the application, it may also change the tests, assertions, mocks, or expected results. Consequently, a passing test suite does not necessarily mean the software still behaves according to the original requirements.

Proctor introduces a separate layer of verification.

Human developers define expected application behavior in human-readable YAML specifications. These specifications are treated as trusted behavioral requirements and are protected from unauthorized modification.

Proctor independently executes these specifications against the actual application and its dependencies.

It checks not only the application's API responses but also relevant effects and state changes in external systems, such as:

- PostgreSQL database records.
- Temporal workflows.
- Application logs.
- Other infrastructure and services as adapters are introduced.

The central principle is:

**AI agents may implement software, but they must not be allowed to silently redefine the behavior against which that software is evaluated.**

Proctor is not intended to guarantee bug-free software or eliminate AI hallucinations. Its purpose is to make incorrect implementations, violated requirements, and unintended behavioral changes significantly harder to introduce unnoticed.

---

# 2. The Problem Proctor Solves

## 2.1 The problem with AI-generated software

Consider a developer asking an AI coding agent:

"Implement project creation. A user must not be able to access another user's projects."

The agent implements the feature but accidentally introduces an authorization vulnerability.

The agent then modifies or generates tests that verify only that:

- The API returns a successful response.
- A project can be created.
- A project can be retrieved.

All tests pass, but the original security requirement is violated.

This is a particularly important problem because AI agents can modify both:

1. The implementation being tested.
2. The tests that are supposed to verify the implementation.

The testing system therefore risks sharing the same assumptions and mistakes as the implementation.

## 2.2 The Proctor approach

Proctor introduces independently maintained specifications.

A human defines the expected behavior:

- Alice must be able to access her own project.
- Alice must not be able to access Bob's project.
- An unauthorized request must return HTTP 403.
- The database must not expose or modify Bob's project.
- The security event must be logged if required.

These expectations are stored outside the application's implementation and are independently executed by Proctor.

The AI agent can change application code, but it cannot silently change the trusted specifications or approve different expected behavior.

## 2.3 The fundamental trust boundary

Proctor should establish three distinct responsibilities:

| Component | Responsibility |
|---|---|
| Human developer | Defines and approves expected behavior |
| AI coding agent | Implements or modifies application code |
| Proctor | Independently verifies actual behavior against approved specifications |

This separation is the foundation of the product.

---

# 3. Product Positioning and Terminology

Proctor should be positioned as a:

**Behavioral Contract Verification System**

Its testing methodology can be described as:

- Specification-Based Testing.
- Black-Box Behavioral Testing.
- Executable Behavioral Specifications.
- Behavioral Contract Verification.

Proctor may perform service-level integration tests, acceptance tests, and end-to-end tests depending on the scenario.

These terms should not be treated as interchangeable:

- **Black-box testing:** Tests behavior through observable interfaces without depending on implementation details.
- **Service-level testing:** Tests an individual service and its interactions with relevant dependencies.
- **End-to-end testing:** Tests a complete workflow across multiple system boundaries.
- **Behavioral contract testing:** Verifies that observable application behavior conforms to explicitly defined requirements.

Proctor's primary identity should be behavioral contract verification, not simply another E2E testing framework.

It should also avoid positioning itself as merely a YAML-based testing framework. YAML is the initial specification format, not the product's primary value.

---

# 4. Product Goals

The initial product should achieve the following:

1. Define expected application behavior in readable, version-controlled specifications.
2. Execute specifications independently of the application's programming language.
3. Test applications through their public HTTP APIs.
4. Verify important side effects in external dependencies.
5. Verify asynchronous operations and eventual consistency.
6. Protect approved specifications from unauthorized modification.
7. Provide detailed evidence explaining why a scenario passed or failed.
8. Integrate into local development workflows and CI/CD pipelines.
9. Make it difficult for AI agents to weaken verification without human authorization.
10. Provide an extensible architecture for adding more verification targets.

## 4.1 Non-goals

Proctor should not initially attempt to:

- Replace unit testing frameworks.
- Replace Go's `testing` package, Jest, pytest, or similar tools.
- Replace existing E2E frameworks.
- Guarantee that an application is completely bug-free.
- Detect every possible AI hallucination.
- Understand every application's internal architecture automatically.
- Support every database, message broker, and infrastructure platform.
- Become a general-purpose application deployment platform.
- Become a general-purpose scripting language.
- Require applications to be written in Go.

Traditional unit and integration tests remain valuable. Proctor adds an independent verification layer.

---

# 5. Language Independence

Although Proctor itself is initially implemented in Go, it must be usable with applications written in different programming languages.

Examples include:

- Go.
- Node.js / TypeScript.
- Python.
- Java.
- Ruby.
- C# / .NET.
- PHP.
- Other languages capable of exposing supported interfaces.

The reason is simple: Proctor tests application behavior, not source code.

For example, a Node.js application can expose REST APIs, connect to PostgreSQL, and run Temporal workflows. Proctor can test all of these without understanding JavaScript or TypeScript.

Likewise, a Python application can be tested using the same YAML specifications and Proctor execution engine.

The application does not need to embed Proctor or implement a special SDK for basic HTTP testing.

The initial architecture should use standard protocols and external connections wherever possible.

Language-specific SDKs or test helpers may be introduced later if they provide meaningful additional capabilities.

---

# 6. Overall Architecture

Proctor should have a modular architecture with a relatively small execution engine and independent verification adapters.

The conceptual architecture is:

```text
                    Human Developer
                          |
                          v
              Human-Approved Specifications
                          |
                          v
                  YAML Specification
                          |
                          v
                 Proctor CLI / Engine
                          |
             +------------+------------+
             |            |            |
             v            v            v
         Validator    Orchestrator   Assertion Engine
             |            |            |
             +------------+------------+
                          |
                          v
                 Verification Adapters
                          |
          +---------------+---------------+
          |               |               |
          v               v               v
       HTTP Client    PostgreSQL       Temporal
          |            Adapter         Adapter
          |               |               |
          v               v               v
       Application    Database        Workflows

                          |
                          v
                    Log Adapter
                          |
                          v
                  Application Logs

                          |
                          v
                Verification Results
                          |
                          v
                Evidence and Reports
```

The architecture must keep the core execution engine independent of individual verification technologies.

For example, PostgreSQL-specific code should not be embedded in the general assertion engine.

Instead, the PostgreSQL adapter should retrieve evidence and return structured results that the common assertion engine can evaluate.

This architecture will make it easier to add Redis, Kafka, RabbitMQ, Elasticsearch, Loki, gRPC, and other technologies later.

---

# 7. Initial Feature Scope: Proctor v1

The first release should focus on four verification targets:

1. HTTP / REST APIs.
2. PostgreSQL.
3. Temporal workflows.
4. Application logs.

It should also include scenario orchestration, assertions, variables, polling, reporting, and specification validation.

Do not attempt to implement every possible integration in the first release.

## 7.1 HTTP / REST API verification

HTTP should be the primary way Proctor interacts with applications.

The initial HTTP adapter should support:

### HTTP methods

- GET
- POST
- PUT
- PATCH
- DELETE

### Request capabilities

- URL and base URL configuration.
- Query parameters.
- Headers.
- Authentication.
- JSON request bodies.
- Form data if practical.
- Request timeout configuration.
- Reusable request variables.
- Response capture.

### Response verification

- HTTP status code.
- Response headers.
- JSON response properties.
- JSONPath-style expressions.
- Expected values.
- Missing and unexpected fields where explicitly configured.
- Array length and element assertions.
- String, numeric, boolean, and null comparisons.

### Multi-step scenarios

A scenario must support multiple sequential operations.

For example:

1. Create a user.
2. Capture the returned user ID.
3. Create a project for that user.
4. Retrieve the project.
5. Verify its ownership.
6. Attempt to access it using another user.
7. Verify that access is rejected.

A single scenario should be able to use the output of previous steps.

### Illustrative specification

```yaml
version: 1

suite: project-management

scenarios:
  - name: create-and-retrieve-project

    steps:
      - name: create project

        request:
          method: POST
          path: /api/projects
          auth: alice
          body:
            name: My Project

        expect:
          status: 201

        save:
          project_id: $.id

      - name: retrieve project

        request:
          method: GET
          path: /api/projects/${project_id}
          auth: alice

        expect:
          status: 200

          json:
            name: My Project
            owner_id: alice
```

This is an illustrative design. The exact schema, expression syntax, and supported assertions must be defined and validated during implementation.

---

## 7.2 PostgreSQL verification

PostgreSQL should be the first database adapter.

The purpose is to verify that API operations produce the expected database state.

For example:

- A project creation request actually creates a database record.
- The project is associated with the correct owner.
- An update changes the intended fields.
- A deletion removes or appropriately marks a record.
- Unauthorized requests do not modify protected records.
- Related database records are created consistently.

### Initial capabilities

- Connect using named database connections.
- Support connection strings and environment variables.
- Execute read-only SQL queries.
- Retrieve query results.
- Assert the number of returned rows.
- Assert individual columns and values.
- Assert null/non-null values.
- Support captured scenario variables in queries.
- Provide query timeouts.
- Support eventual verification through bounded polling.

### Illustrative specification

```yaml
verify:
  postgres:
    connection: test_database

    query: |
      SELECT name, owner_id
      FROM projects
      WHERE id = '${project_id}'

    expect:
      rows: 1

      first_row:
        name: My Project
        owner_id: alice
```

### Important security requirement

The initial PostgreSQL adapter should be read-only.

Proctor should inspect application state rather than modifying database records directly.

The application should be responsible for performing the operation being tested.

This avoids tests passing because Proctor itself created or modified the expected data.

The implementation should enforce read-only access at the database connection level where possible, rather than relying solely on a configuration convention.

It should also apply query timeouts and protect against unsafe interpolation of captured variables.

Prefer parameterized query support for dynamic values.

Do not allow arbitrary destructive SQL in trusted specifications as a default capability.

---

## 7.3 Temporal workflow verification

Temporal is an important initial integration because many modern applications use asynchronous workflows.

An API request may return successfully while the actual operation is still running.

For example:

1. A user requests an export.
2. The API returns HTTP 202.
3. The application starts a Temporal workflow.
4. The workflow performs the export.
5. The workflow completes.
6. The application stores the resulting information in PostgreSQL.

Verifying only the initial HTTP response is insufficient.

Proctor should be able to independently inspect Temporal and verify the workflow's state.

### Initial capabilities

- Connect to a configured Temporal server.
- Configure namespace.
- Identify workflows by workflow ID.
- Verify that a workflow exists.
- Verify workflow execution status.
- Verify successful completion.
- Detect workflow failures.
- Apply bounded polling while awaiting a terminal state.
- Capture relevant workflow information for reports.

### Illustrative specification

```yaml
verify:
  temporal:
    connection: test_temporal
    namespace: default
    workflow_id: "${workflow_id}"

    eventually:
      timeout: 60s
      interval: 1s

    expect:
      status: completed
```

For v1, prioritize workflow existence and execution status.

Detailed workflow-history inspection, activity-level assertions, and more advanced Temporal capabilities can be introduced later.

Do not assume that every application uses the same workflow naming conventions or ID-generation strategy.

The specification must be able to reference workflow IDs returned by previous API operations.

---

## 7.4 Application log verification

Proctor should verify important application events and their associated information.

For example, when a project is created, the application might generate:

```json
{
  "level": "info",
  "event": "project.created",
  "project_id": "123",
  "user_id": "456"
}
```

Proctor should verify:

- Whether the expected event was logged.
- Whether the correct log level was used.
- Whether important fields contain the expected values.
- Whether the event is associated with the correct request or business operation.
- Whether an unexpected error event occurred, when explicitly specified.

### Initial log sources

Start with:

1. Local JSON log files.
2. Docker container logs, if practical.

Structured JSON logs should be preferred over plain-text matching.

The log adapter should support:

- Filtering by event name.
- Filtering by log level.
- Matching structured fields.
- Time windows.
- Correlation IDs.
- Bounded polling for asynchronous log events.

### Illustrative specification

```yaml
verify:
  logs:
    source: application

    eventually:
      timeout: 10s
      interval: 500ms

    expect:
      event: project.created
      level: info

      fields:
        project_id: "${project_id}"
        user_id: alice
```

The log adapter must distinguish between an event not yet observed and an event that cannot be retrieved because the configured source is unavailable.

A missing log should not automatically be reported as a failed application assertion if Proctor could not access the log source.

The report must distinguish infrastructure or adapter errors from actual behavioral failures.

---

# 8. Scenario Execution and Cross-System Verification

One of Proctor's most important capabilities is verifying an operation across multiple systems.

A single business scenario may involve:

- An HTTP request.
- A database modification.
- A Temporal workflow.
- An application log event.

Proctor must allow these checks to belong to the same scenario.

### Example: Project export

The expected workflow is:

1. A user requests a project export.
2. The application returns HTTP 202.
3. A Temporal workflow starts.
4. The workflow completes.
5. The database reflects the completed export.
6. An export-completed log event is generated.

Illustrative specification:

```yaml
version: 1

suite: project-export

scenarios:

  - name: project-export-completes-successfully

    steps:

      - name: request export

        request:
          method: POST
          path: /api/projects/${project_id}/export
          auth: alice

        expect:
          status: 202

        save:
          workflow_id: $.workflow_id
          export_id: $.export_id

      - name: verify workflow

        verify:
          temporal:
            connection: test_temporal
            workflow_id: "${workflow_id}"

            eventually:
              timeout: 60s
              interval: 1s

            expect:
              status: completed

      - name: verify database

        verify:
          postgres:
            connection: test_database

            query: |
              SELECT status
              FROM exports
              WHERE id = '${export_id}'

            eventually:
              timeout: 15s
              interval: 500ms

            expect:
              rows: 1

              first_row:
                status: completed

      - name: verify completion event

        verify:
          logs:
            source: application

            eventually:
              timeout: 10s
              interval: 500ms

            expect:
              event: project.export.completed

              fields:
                export_id: "${export_id}"
```

This is an example of the desired product capability, not a finalized YAML schema.

The final schema should be consistent, easy to read, unambiguous, and straightforward to validate.

---

# 9. Eventual Consistency and Asynchronous Verification

This feature is essential and should be part of the initial execution engine.

Distributed applications do not always update every dependency immediately.

For example:

- The HTTP request returns immediately.
- A Temporal workflow starts.
- The database is updated after two seconds.
- A log event is emitted afterward.

If Proctor checks the database immediately, it may report a false failure.

Instead, Proctor should support explicit bounded polling.

Example:

```yaml
eventually:
  timeout: 15s
  interval: 500ms
```

The engine should:

1. Execute the verification.
2. Evaluate the expected condition.
3. If the condition is not satisfied, wait for the configured interval.
4. Repeat until the condition succeeds or the timeout expires.
5. Record execution time and the final observed state.
6. Produce a useful failure message if the timeout expires.

### Important distinctions

A timeout should not be used to hide genuine failures.

For example:

- A PostgreSQL connection failure is an infrastructure error.
- An invalid SQL query is a specification or execution error.
- A query returning no rows may be an assertion failure.
- A condition that becomes true after several seconds is a successful eventual verification.
- A condition that never becomes true within the allowed timeout is a behavioral failure.

The implementation must distinguish these cases.

Retries must be bounded and predictable.

---

# 10. Specification Design Principles

YAML is the initial format because it is readable, familiar, and easy to version-control.

However, Proctor must not become an unrestricted scripting language.

The specification language should be declarative.

A specification describes:

- Preconditions.
- Actions.
- Expected outcomes.
- Verification targets.
- Assertions.
- Time constraints.
- Dependencies between steps.

It should not become a general-purpose programming language.

### 10.1 Human readability

A developer should be able to read a scenario and understand the intended business behavior without knowing the implementation.

### 10.2 Explicit assertions

A test should not pass merely because an operation completed without an error.

Expected results must be explicitly stated.

For example:

Bad:

```yaml
expect:
  status: 200
```

Better:

```yaml
expect:
  status: 200

  json:
    project:
      owner_id: alice
      status: active
```

The appropriate assertions depend on the requirement. Not every response needs to assert every field.

### 10.3 Explicit negative scenarios

Security and authorization requirements must include negative cases.

For example:

- A user cannot access another user's project.
- An unauthenticated user cannot create a project.
- A blocked session cannot push code.
- A failed review cannot be treated as approved.
- A deleted resource cannot be retrieved.

### 10.4 Avoid excessive flexibility

Do not initially support arbitrary embedded JavaScript, Python, shell scripts, or custom executable expressions.

The goal is to make the specifications predictable and auditable.

If custom extensions become necessary, introduce a controlled extension mechanism later.

---

# 11. Protecting Human-Approved Specifications

This is one of Proctor's defining features.

The specifications are only useful as an independent trust boundary if AI agents cannot silently weaken them.

The system should support a distinction between:

1. Creating or modifying a proposed specification.
2. Approving the expected behavior that will be enforced.

An AI agent may propose specification changes, but it must not automatically approve them.

### 11.1 Specification integrity

The product should support mechanisms to:

- Identify approved specifications.
- Detect modifications to approved specifications.
- Report which specifications changed.
- Detect removed scenarios.
- Detect weakened or removed assertions where practical.
- Require an explicit approval process for changes to trusted expectations.

### 11.2 Approval workflow

A proposed workflow:

```text
Human-approved specification
            |
            v
      AI modifies code
            |
            v
      Proctor executes
            |
       +----+----+
       |         |
       v         v
     PASS      FAIL
       |         |
       v         v
   Continue    Investigate
                 |
                 v
       Proposed specification
            change?
                 |
                 v
        Human reviews change
                 |
                 v
        Human explicitly approves
```

The system must never automatically approve changed expected results simply because the current implementation fails.

### 11.3 Approval and baseline changes

Some tests may eventually support approved output baselines or snapshots.

For example, an API response may contain a large document or structured output.

Proctor should distinguish:

- Generating a proposed new baseline.
- Reviewing the difference.
- Accepting the new baseline.

Generating a candidate must not automatically change the trusted expected result.

### 11.4 Git integration

For the initial version, use Git as the underlying version-control and review mechanism where practical.

Proctor should be able to validate the integrity of approved specifications.

A later release could provide more sophisticated approval policies and CI enforcement.

Do not assume that a YAML file is protected merely because it is stored in a Git repository. CI permissions, branch protection, review requirements, and the approval mechanism must also be considered.

---

# 12. CLI Design

Proctor should provide a standalone executable written in Go.

The CLI is the entry point into the Proctor engine.

It is not the primary value of the product. The value comes from the reusable verification engine and its independent behavioral specifications.

A standalone CLI makes Proctor usable across programming languages and build systems.

Examples:

- Go projects can invoke it through Make.
- Node.js projects can invoke it through `package.json`.
- Python projects can invoke it through their development tooling.
- CI systems can invoke the same executable directly.

### Proposed commands

#### Validate

```bash
proctor validate
```

Responsibilities:

- Read configuration.
- Discover specifications.
- Validate YAML syntax.
- Validate specification schema.
- Check required fields.
- Validate supported actions and assertions.
- Detect invalid variable references where possible.
- Detect duplicate scenario identifiers.
- Report specification errors.

Validation must not execute application operations.

#### Run

```bash
proctor run
```

Responsibilities:

- Load configuration.
- Load approved specifications.
- Establish configured connections.
- Execute scenarios.
- Evaluate assertions.
- Record evidence.
- Produce reports.
- Return a non-zero exit code when verification fails or execution cannot be completed.

#### Report

```bash
proctor report
```

Responsibilities:

- Display or export the most recent execution report.
- Support machine-readable output.
- Include failures, evidence, and execution information.

#### Diff

```bash
proctor diff
```

Responsibilities:

- Show changes between the current specifications and an approved baseline.
- Highlight added or removed scenarios.
- Highlight assertion changes.
- Help reviewers understand behavioral contract changes.

#### Approve

```bash
proctor approve
```

This should be introduced only when the approval mechanism is properly defined.

It must require explicit authorization and must not be callable by an ordinary AI execution flow without the appropriate approval controls.

### Integration example: Node.js

A Node.js project could add:

```json
{
  "scripts": {
    "test:behavior": "proctor run"
  }
}
```

Then:

```bash
npm run test:behavior
```

### Integration example: Go

A Go project could use:

```makefile
.PHONY: test-behavior

test-behavior:
	proctor validate
	proctor run
```

### Integration example: CI

```yaml
name: Proctor Verification

on:
  pull_request:

jobs:
  behavioral-verification:
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: Validate specifications
        run: proctor validate

      - name: Run verification
        run: proctor run
```

The actual workflow will need to start the application and its dependencies before Proctor runs.

Docker Compose can be used by the application repository to manage this test environment.

Proctor should not initially become responsible for building and deploying every application it tests.

---

# 13. Project Configuration

A project should have a central configuration file.

Proposed filename:

`proctor.yaml`

Illustrative configuration:

```yaml
version: 1

project: example-application

target:
  base_url: http://localhost:8080

specifications:
  - specs/**/*.yaml

connections:

  postgres:
    test_database:
      dsn_env: PROCTOR_POSTGRES_DSN

  temporal:
    test_temporal:
      address: localhost:7233
      namespace: default

  logs:
    application:
      type: file
      path: ./logs/application.jsonl

execution:
  timeout: 30s
  parallel: false

report:
  formats:
    - terminal
    - json

  directory: ./proctor-reports
```

This is a proposed configuration structure, not a final schema.

Design requirements:

- Secrets must not be committed to source control.
- Environment variables should be supported.
- Different environments should be configurable.
- Connection names should be reusable by specifications.
- Configuration validation should happen before execution.
- Timeouts should be explicit.
- The default execution mode should favor determinism and reliability over speed.

The initial release should support local and CI configurations.

---

# 14. Suggested Directory Structure in an Application Repository

Proctor should be a separate project and executable.

Applications using Proctor should add their own specification directory.

For example:

```text
my-application/
│
├── cmd/
├── internal/
├── api/
├── configs/
├── scripts/
│
├── proctor/
│   ├── proctor.yaml
│   │
│   ├── specs/
│   │   ├── authentication.yaml
│   │   ├── users.yaml
│   │   ├── project-management.yaml
│   │   ├── permissions.yaml
│   │   ├── background-workflows.yaml
│   │   └── security-boundaries.yaml
│   │
│   ├── fixtures/
│   │   └── test-data.yaml
│   │
│   └── environments/
│       ├── local.yaml
│       └── ci.yaml
│
├── .github/
│   └── workflows/
│       └── proctor.yml
│
├── Dockerfile
├── compose.yaml
├── Makefile
└── go.mod
```

This directory structure is illustrative.

Proctor should not force applications to follow a particular source-code structure.

The only essential requirement is that the application provides the interfaces and test environment needed to execute the specifications.

---

# 15. Suggested Proctor Source Repository Structure

The Proctor repository itself could initially be organized as follows:

```text
proctor/
│
├── cmd/
│   └── proctor/
│       └── main.go
│
├── internal/
│   │
│   ├── cli/
│   │
│   ├── config/
│   │
│   ├── specification/
│   │
│   ├── validation/
│   │
│   ├── execution/
│   │
│   ├── scenario/
│   │
│   ├── variables/
│   │
│   ├── assertions/
│   │
│   ├── adapters/
│   │   ├── http/
│   │   ├── postgres/
│   │   ├── temporal/
│   │   └── logs/
│   │
│   ├── polling/
│   │
│   ├── approval/
│   │
│   └── reporting/
│
├── schemas/
│   └── specification.schema.json
│
├── examples/
│   ├── go-http-service/
│   ├── node-http-service/
│   └── python-http-service/
│
├── docs/
│
├── tests/
│
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

This is a proposed starting point.

Do not create abstractions or packages without a concrete need. Keep the implementation modular, but avoid unnecessary framework complexity.

---

# 16. Reliability and Trustworthiness

Proctor must be designed carefully because its results may be used as evidence that AI-generated code conforms to approved requirements.

However, Proctor cannot guarantee that software is bug-free.

Its reliability depends on:

- The completeness and correctness of the human-approved specifications.
- Whether the scenarios exercise the relevant behavior.
- The correctness of Proctor's execution engine.
- The correctness of its adapters.
- The quality of its assertions.
- The integrity of the approval and CI process.

### 16.1 What Proctor can detect

Provided the relevant behavior is specified and exercised, Proctor can detect:

- API responses that violate requirements.
- Incorrect authorization behavior.
- Missing database records.
- Unexpected database modifications.
- Failed asynchronous workflows.
- Missing expected log events.
- Incorrect business-state transitions.
- Violations of expected behavior after AI code changes.

### 16.2 What Proctor cannot guarantee

Proctor cannot automatically detect:

- Requirements that were never specified.
- Bugs in untested execution paths.
- Incorrect human assumptions.
- Every possible security vulnerability.
- Every possible race condition.
- Correctness of internal implementation mechanisms that are not observable.
- Every AI hallucination.

The product should never claim 100% reliability or 100% hallucination prevention.

A better product promise is:

**Proctor independently verifies whether observable software behavior conforms to human-approved requirements.**

### 16.3 Independent verification

Proctor must execute against the actual application and relevant dependencies.

It must not simply trust:

- AI-generated test results.
- Mocked responses presented as actual application results.
- Application-generated claims that an operation succeeded.
- Assertions generated automatically from the implementation being tested.

Mocks and simulators may be useful for controlled test environments, but they must not be confused with verification against real dependencies.

### 16.4 Mutation testing

Consider adding mutation testing as a way to evaluate the effectiveness of Proctor specifications.

For example, intentionally introduce a bug:

- Change an authorization check.
- Remove a database write.
- Skip a Temporal workflow.
- Change a response status.
- Suppress an expected log event.

Then execute Proctor and check whether the specifications detect the introduced fault.

This helps measure how effective the verification suite is.

Mutation testing does not prove complete correctness, but it provides useful evidence that the tests can detect certain classes of defects.

It could be introduced after the initial execution engine is stable.

---

# 17. Security Requirements

Because Proctor may connect to databases and infrastructure, security must be considered from the beginning.

Initial requirements:

1. Never commit credentials into specifications.
2. Support environment-variable-based secrets.
3. Do not print credentials in execution reports.
4. Use read-only PostgreSQL connections initially.
5. Apply connection and query timeouts.
6. Avoid arbitrary shell execution from YAML.
7. Avoid unrestricted embedded scripts.
8. Validate specification inputs.
9. Prevent unsafe SQL interpolation.
10. Make approval operations explicit.
11. Distinguish infrastructure failures from behavioral failures.
12. Do not allow AI agents to silently change trusted specifications.
13. Ensure reports do not accidentally expose sensitive application data.
14. Avoid automatically following arbitrary external URLs or executing untrusted requests without an explicit configuration policy.

The YAML specification must not become an unrestricted execution environment.

---

# 18. Reporting

A successful or failed execution should produce useful evidence.

A report should include:

- Project name.
- Execution identifier.
- Timestamp.
- Environment.
- Specification version or integrity information.
- Scenarios executed.
- Individual steps.
- Assertions evaluated.
- Passed assertions.
- Failed assertions.
- Execution duration.
- Relevant response data.
- Database verification results.
- Temporal workflow information.
- Relevant log evidence.
- Infrastructure and adapter errors.

Example terminal output:

```text
PROCTOR VERIFICATION

Project: example-application

Scenarios:  12
Passed:     10
Failed:      2

--------------------------------------------------

PASS  Create project

  HTTP status: 201
  Database record: found
  Owner: alice

--------------------------------------------------

PASS  Export project

  HTTP status: 202
  Temporal workflow: completed
  Database status: completed
  Completion event: found

--------------------------------------------------

FAIL  Prevent unauthorized project access

  Expected:
    HTTP status: 403

  Actual:
    HTTP status: 200

  Scenario:
    security-boundaries.yaml

--------------------------------------------------

RESULT: FAILED
```

The report must be understandable to both developers and AI coding agents.

A machine-readable JSON report is particularly important because AI agents and CI systems should be able to consume the results.

Reports should distinguish:

- PASS: All required assertions succeeded.
- FAIL: A behavioral assertion failed.
- ERROR: Proctor could not reliably execute or evaluate the check.
- SKIPPED: A scenario was intentionally not executed under the current configuration.

Do not treat infrastructure errors as successful tests.

---

# 19. Determinism and Flakiness

Trustworthy verification must be predictable.

Proctor should avoid introducing flaky behavior through its own execution engine.

Important design principles:

- Explicit timeouts.
- Bounded polling.
- Controlled retries.
- Clear error classification.
- Predictable variable scope.
- Isolated scenario state where appropriate.
- Deterministic execution order by default.
- No automatic retry of behavioral failures unless the specification explicitly calls for eventual verification.
- Clear reporting of how long an assertion took to succeed.

Parallel execution can be introduced, but it should not be the default until scenario isolation is properly designed.

Tests that share database records, users, workflows, or external resources must not interfere with one another.

The engine should support unique test identifiers and resource cleanup strategies in a controlled way.

---

# 20. Extensibility

The architecture should support future adapters without requiring changes to the core execution engine.

Potential future verification targets:

| Technology | Possible verification |
|---|---|
| Redis | Cache values, expiration, state |
| Kafka | Message production and consumption |
| RabbitMQ | Queue and message behavior |
| Elasticsearch | Indexed documents and queries |
| Loki | Centralized log verification |
| gRPC | RPC requests and responses |
| WebSocket | Event and message behavior |
| S3-compatible storage | Object creation and metadata |
| Filesystem | File creation, content, and permissions |

These are future possibilities, not v1 requirements.

The adapter architecture should allow new integrations to be added independently.

The core engine should own:

- Scenario orchestration.
- Variable handling.
- Assertion evaluation.
- Polling.
- Execution lifecycle.
- Evidence collection.
- Reporting.

Adapters should own:

- Connection establishment.
- Technology-specific operations.
- Retrieval of observable evidence.
- Conversion into common result structures.
- Resource cleanup where necessary.

---

# 21. Proposed Implementation Roadmap

Implementation should proceed incrementally.

Do not attempt to implement every feature in one large change.

## Phase 1: Foundation

Implement:

- Go project structure.
- CLI entry point.
- Configuration loading.
- YAML specification loading.
- Specification validation.
- Scenario model.
- Execution result model.
- Basic terminal reporting.
- JSON reporting.

Acceptance criteria:

- Proctor can load a project configuration.
- Proctor can discover YAML specifications.
- Invalid specifications produce understandable errors.
- Valid specifications can be represented as internal scenario models.
- `proctor validate` works without an application running.

## Phase 2: HTTP execution

Implement:

- HTTP client.
- Request configuration.
- Response handling.
- Authentication.
- JSON assertions.
- Variables and captured response values.
- Multi-step scenarios.
- Timeouts.

Acceptance criteria:

- Proctor can execute a complete scenario against a sample HTTP application.
- A scenario can capture a value from one response and use it in a later request.
- Failed assertions are clearly reported.
- The command exits with a non-zero status when verification fails.

## Phase 3: PostgreSQL adapter

Implement:

- Named connections.
- Environment-based credentials.
- Read-only database connections.
- Parameterized queries.
- Query result handling.
- Assertions against query results.
- Timeouts.

Acceptance criteria:

- Proctor can verify that an API operation created the correct database record.
- Proctor can detect missing or incorrect records.
- Proctor cannot modify database state through the initial adapter.

## Phase 4: Polling and eventual consistency

Implement:

- Bounded polling.
- Configurable interval.
- Configurable timeout.
- Retryable verification conditions.
- Proper error classification.
- Polling evidence in reports.

Acceptance criteria:

- Proctor can wait for a database condition to become true.
- Proctor succeeds when the condition becomes true within the timeout.
- Proctor fails with useful evidence when it does not.
- Connection failures are not incorrectly reported as ordinary assertion failures.

## Phase 5: Temporal adapter

Implement:

- Temporal connection configuration.
- Namespace configuration.
- Workflow lookup.
- Workflow status verification.
- Bounded polling.
- Workflow evidence in reports.

Acceptance criteria:

- Proctor can verify that an API-triggered workflow exists.
- Proctor can verify successful completion.
- Proctor can identify failed or missing workflows.

## Phase 6: Log adapter

Implement:

- JSON log parsing.
- File-based log source.
- Docker log source if practical.
- Event matching.
- Structured field assertions.
- Time windows.
- Correlation support.
- Bounded polling.

Acceptance criteria:

- Proctor can verify an expected event.
- Proctor can check event fields.
- Proctor can detect a missing event after the configured timeout.
- Log-source failures are distinguishable from missing expected events.

## Phase 7: Specification integrity and approval

Implement:

- Approved specification identification.
- Specification change detection.
- Scenario and assertion diffing.
- Proposed change workflow.
- Explicit approval.
- CI enforcement guidance.

Acceptance criteria:

- Proctor can identify changes to approved specifications.
- Proctor can show meaningful differences.
- An AI agent cannot silently approve changed expected behavior through the normal execution path.
- Human approval is explicit and auditable.

## Phase 8: Documentation and release

Implement:

- Installation instructions.
- Quick-start guide.
- YAML specification reference.
- Configuration reference.
- HTTP examples.
- PostgreSQL examples.
- Temporal examples.
- Log examples.
- Node.js integration example.
- Python integration example.
- Go integration example.
- CI integration guide.
- Security documentation.

Publish a usable initial release with reproducible installation instructions.

---

# 22. Testing Proctor Itself

Proctor must be tested independently.

It would be ironic to create a system intended to provide independent verification and then fail to verify its own behavior.

The Proctor repository should include:

1. Unit tests for parsers and validators.
2. Unit tests for the assertion engine.
3. Tests for variable interpolation and scoping.
4. Tests for HTTP request execution.
5. Tests for timeout behavior.
6. Tests for polling behavior.
7. PostgreSQL integration tests.
8. Temporal integration tests.
9. Log adapter tests.
10. Tests for malformed and malicious specifications.
11. Tests for error classification.
12. Tests for report correctness.
13. Tests for specification integrity and approval rules.

The project should also include example applications in multiple programming languages to demonstrate language independence.

The same Proctor specifications should ideally be usable against equivalent example applications implemented in Go, Node.js, and Python.

This is an important architectural validation.

---

# 23. Key Design Decisions to Resolve During Implementation

The following decisions should be explicitly evaluated rather than assumed.

### Specification syntax

- How should scenarios, steps, and verification operations be represented?
- Should HTTP and dependency verification use a unified step model?
- What expression syntax should be used for variables and JSONPath?
- How should authentication profiles be configured?
- How should fixtures and test data be represented?

Prefer the simplest consistent syntax.

### Variable handling

- How are variables captured?
- Are variables scenario-scoped?
- Can suites share variables?
- How are secrets handled?
- How are dynamic values represented?

Default to scenario-level isolation.

### Database verification

- How are query parameters represented?
- How are SQL results converted to assertion values?
- How are connection permissions enforced?
- How should database cleanup work?

Keep the first version read-only.

### Temporal verification

- How should workflow IDs be captured?
- How should workflow completion be represented?
- What should happen when a workflow remains running until timeout?
- Should workflow history be supported immediately or later?

Start with workflow existence and status.

### Logs

- What is the minimum supported log format?
- How are events correlated with a scenario?
- How are logs read without accidentally consuming or deleting them?
- How should Docker logs be accessed?

Prefer structured JSON logs and read-only access.

### Specification protection

- How is the approved baseline identified?
- How are changes approved?
- How are approval permissions enforced?
- How should approval work in CI?

The implementation must not treat the presence of a Git commit as sufficient proof of human approval.

### Execution isolation

- How are scenario resources isolated?
- How are generated IDs handled?
- How is cleanup performed?
- When can parallel execution safely be enabled?

Favor deterministic execution initially.

---

# 24. Product Principles

The following principles should guide all implementation decisions.

## Principle 1: Independent verification

Proctor must be independent of the application implementation and the AI agent modifying it.

## Principle 2: Human-owned expected behavior

AI may propose changes to requirements, but cannot silently approve changes to trusted expectations.

## Principle 3: Verify actual effects

A successful HTTP response is not sufficient when the requirement includes database changes, asynchronous workflows, or observable events.

## Principle 4: Language independence

Proctor should work with applications written in different languages without requiring those applications to adopt Proctor's implementation language.

## Principle 5: Explicit assertions

A scenario must clearly express what must be true.

## Principle 6: Evidence-based results

Every failure should explain what was expected, what actually happened, and where the evidence came from.

## Principle 7: Safe by default

Read-only verification, controlled execution, explicit permissions, and secure secret handling should be the defaults.

## Principle 8: Simple specifications

A developer should be able to understand a scenario without becoming familiar with a complicated DSL.

## Principle 9: Extensible adapters

New verification technologies should be addable without redesigning the execution engine.

## Principle 10: No false guarantees

Proctor should never claim to guarantee that software is bug-free or that AI hallucinations are impossible.

---

# 25. Final Product Vision

The long-term vision is to make Proctor an independent verification layer for modern software development, particularly AI-assisted software development.

Imagine a development workflow:

1. A human defines the required behavior.
2. Proctor specifications capture that behavior.
3. An AI coding agent implements the application.
4. The application is started in a controlled environment.
5. Proctor executes the approved specifications.
6. Proctor checks API responses, database state, asynchronous workflows, and important events.
7. Proctor produces an independent verification report.
8. If the application violates an approved requirement, the build fails.
9. If requirements genuinely change, a human reviews and approves the specification change.

The AI agent is free to change the implementation, refactor the architecture, replace libraries, or rewrite components.

But it must preserve the externally observable behavior that humans have approved.

This is the fundamental value proposition:

**The implementation can change. The approved behavioral contract remains the authority.**

Proctor does not prove that software is perfect.

It makes it significantly harder for incorrect behavior to become accepted merely because an AI agent says the implementation and its tests are correct.

---

# 26. Instructions to the Coding Agent

Use this document as the product vision and initial technical requirements for implementing Proctor.

Before writing substantial code:

1. Review the entire specification.
2. Identify ambiguities and technical risks.
3. Propose a concrete initial architecture.
4. Define the first version of the YAML schema.
5. Define the core execution and adapter interfaces.
6. Define error categories and reporting structures.
7. Produce an incremental implementation plan.

Then implement incrementally.

Important implementation instructions:

- Use Go for the initial implementation.
- Keep the application-facing specification language-independent.
- Prioritize a small, reliable core engine.
- Start with HTTP execution.
- Add PostgreSQL, Temporal, and log verification through separate adapters.
- Do not implement every future integration.
- Avoid unnecessary abstractions and over-engineering.
- Do not introduce arbitrary script execution into YAML.
- Do not allow AI-driven automatic approval of changed specifications.
- Make errors and execution results explicit.
- Write tests for Proctor itself.
- Provide working examples in multiple application languages.
- Document every implemented specification feature.
- Keep the actual implemented YAML schema consistent with its documentation.

**Do not attempt to implement the entire product in a single step.** Build a working vertical slice first: a human-approved YAML scenario that sends an HTTP request, verifies the response, and produces a trustworthy report. Then expand the same execution model to PostgreSQL, Temporal, and logs.

The initial success criterion is not the number of supported features. It is whether Proctor can independently and reliably verify a real application against a human-approved behavioral specification.