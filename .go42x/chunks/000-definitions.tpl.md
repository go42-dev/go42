### Definitions

Definitions give terms a shared meaning throughout these instructions. Use them
when interpreting rules, deciding whether an action needs confirmation, and
deciding what to keep or clean up. Read each definition together with the scope
and conditions of the rule that uses it. Rules determine which actions are
required or allowed.

#### Instruction words

These words express how strictly to follow a rule. Their meaning is the same
regardless of capitalization or Markdown formatting.

- Must, required: Follow the rule whenever it applies.

- Always: Follow the rule every time its stated conditions apply.

- Must not, never, do not: The action is prohibited whenever the rule applies.

- Should, recommended: Follow the rule by default. Depart from it only for a
  clear reason relevant to the task, and explain that reason.

- Should not, discouraged: Avoid the action by default. Take it only for a
  clear reason relevant to the task, and explain that reason.

- Prefer: Choose the stated option when suitable. Choose an alternative when
  it better meets the task's requirements.

- May, optional: The action is permitted but not required. Other applicable
  restrictions still apply.

- Can: Describes an ability or possibility. Permission comes from the applicable
  instructions and the user's request.

Direct instructions such as "Use", "Run", and "Keep" express requirements unless
qualified by wording such as "should", "may", or "prefer".

Example: "Never commit changes unless explicitly instructed" prohibits commits
until the user explicitly requests one. The condition is part of the rule.

#### Terms

- Prompt: A message given directly to the agent containing instructions, a
  request, or context. Pasted code and quoted content are part of that prompt.

- External system: A remote or shared service or managed environment accessed
  during a task, such as a repository hosting service, a container registry, or
  a Kubernetes cluster.

- State change: An action that alters stored data, configuration, published
  content, or running resources. Examples include editing a local file, pushing
  an image, changing repository settings, or restarting a pod. The applicable
  rule determines whether the change requires confirmation.

- Operational resource: An existing resource that supports normal system
  operation, such as an application pod, a deployment, or a database. Such
  resources can serve development, staging, or production environments.

- Isolated test resource: A temporary resource created for testing that can be
  created, used, and removed without changing existing system configuration or
  operational resources or disrupting normal operation. Examples include a
  separate test pod, an isolated test container, or a temporary port-forwarding
  process.

- Intermediate file: Working material used to complete a task, such as a scratch
  script, diagnostic log, or draft output. Its continued usefulness determines
  whether it can be cleaned up under the intermediate storage rules.

- Deliverable: An output requested by the user or needed to review the completed
  work, such as a final report, patch, or generated document. A file remains a
  deliverable even when stored in the intermediate directory.
