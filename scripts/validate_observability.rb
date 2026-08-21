#!/usr/bin/env ruby

require "json"
require "yaml"

RULES_PATH = File.expand_path("../deploy/observability/prometheus-rules.yml", __dir__)
DASHBOARD_PATH = File.expand_path("../deploy/observability/grafana-dashboard.json", __dir__)
PROMETHEUS_PATH = File.expand_path("../deploy/observability/prometheus.yml", __dir__)

REQUIRED_SCRAPE_TARGETS = {
  "api" => "api:8080",
  "agent-worker" => "agent-worker:9467"
}.freeze

REQUIRED_RETRY_ALERTS = {
  "AICompanionAgentRetryStorm" => {
    "severity" => "page",
    "for" => "1m",
    "fragments" => [
      "ai_companion_agent_execution_retries_recent",
      'outcome="scheduled"',
      ">= 20"
    ]
  },
  "AICompanionAgentRetryRecoveryLow" => {
    "severity" => "page",
    "for" => "1m",
    "fragments" => [
      "ai_companion_agent_execution_retries_recent",
      "ai_companion_agent_execution_retry_recovery_ratio",
      ">= 5",
      "< 0.8",
      "and on()"
    ]
  },
  "AICompanionAgentRetryExhausted" => {
    "severity" => "ticket",
    "for" => "1m",
    "fragments" => [
      "ai_companion_agent_execution_retries_recent",
      "exhausted|deadline_exhausted",
      ">= 3"
    ]
  }
}.freeze

REQUIRED_AGENT_DISPATCH_ALERTS = {
  "AICompanionAgentWorkerUnavailable" => {
    "severity" => "page",
    "for" => "2m",
    "fragments" => [
      'up{job="agent-worker"} == 0',
      "ai_companion_agent_worker_up == 0",
      "or"
    ]
  },
  "AICompanionAgentToolWakeLatencyHigh" => {
    "severity" => "page",
    "for" => "2m",
    "fragments" => [
      "histogram_quantile(0.95",
      "ai_companion_agent_tool_wake_latency_seconds_bucket",
      "ai_companion_agent_tool_wake_latency_seconds_count",
      "> 1",
      ">= 5",
      "and on()"
    ]
  },
  "AICompanionAgentDispatchReplayStorm" => {
    "severity" => "ticket",
    "for" => "2m",
    "fragments" => [
      "ai_companion_agent_dispatch_hints_total",
      'outcome="replay_requested"',
      "> 0.25",
      ">= 20",
      "and on()"
    ]
  }
}.freeze

REQUIRED_SCHEDULER_ALERTS = {
  "AICompanionDeploymentSchedulerUnavailable" => {
    "severity" => "page",
    "for" => "2m",
    "fragments" => [
      'up{job="deployment-scheduler-sandbox"} == 0',
      "ai_companion_deployment_scheduler_up == 0",
      "or"
    ]
  },
  "AICompanionDeploymentSchedulerFailures" => {
    "severity" => "page",
    "for" => "1m",
    "fragments" => [
      "ai_companion_deployment_scheduler_consecutive_failures",
      ">= 3"
    ]
  },
  "AICompanionDeploymentSchedulerCertificationExpiring" => {
    "severity" => "ticket",
    "for" => "5m",
    "fragments" => [
      "ai_companion_deployment_scheduler_certification_expiry_timestamp_seconds",
      "time()",
      "< 900"
    ]
  },
  "AICompanionDeploymentSchedulerBacklog" => {
    "severity" => "page",
    "for" => "5m",
    "fragments" => [
      "ai_companion_deployment_scheduler_due",
      ">= 10",
      "ai_companion_deployment_scheduler_overdue_deadline",
      "> 0",
      "or"
    ]
  }
}.freeze

REQUIRED_SHADOW_ALERTS = {
  "AICompanionDeploymentShadowUnavailable" => {
    "severity" => "page",
    "for" => "2m",
    "fragments" => [
      'up{job="deployment-shadow-preprod"} == 0',
      "ai_companion_deployment_shadow_up == 0",
      "or"
    ]
  },
  "AICompanionDeploymentShadowFailures" => {
    "severity" => "page",
    "for" => "1m",
    "fragments" => [
      "ai_companion_deployment_shadow_consecutive_failures",
      ">= 3"
    ]
  },
  "AICompanionDeploymentShadowDrift" => {
    "severity" => "ticket",
    "for" => "5m",
    "fragments" => [
      "ai_companion_deployment_shadow_drift",
      "> 0"
    ]
  },
  "AICompanionDeploymentShadowLookupErrors" => {
    "severity" => "ticket",
    "for" => "5m",
    "fragments" => [
      "ai_companion_deployment_shadow_lookup_errors",
      "> 0"
    ]
  }
}.freeze

REQUIRED_DASHBOARD_EXPRESSIONS = {
  "retry outcomes" => ["ai_companion_agent_execution_retries_recent"],
  "sample-guarded retry recovery" => [
    "ai_companion_agent_execution_retry_recovery_ratio",
    "ai_companion_agent_execution_retries_recent",
    ">= 5",
    "and on()"
  ],
  "agent dispatch hints" => ["ai_companion_agent_dispatch_hints_total"],
  "agent dispatch queue depth" => ["ai_companion_agent_dispatch_queue_depth"],
  "agent dispatch in flight" => ["ai_companion_agent_dispatch_in_flight"],
  "agent tool wake outcomes" => ["ai_companion_agent_tool_wake_events_total"],
  "sample-guarded agent tool wake latency" => [
    "histogram_quantile(0.95",
    "ai_companion_agent_tool_wake_latency_seconds_bucket",
    "ai_companion_agent_tool_wake_latency_seconds_count",
    ">= 5",
    "and on()"
  ],
  "deployment scheduler up" => ["ai_companion_deployment_scheduler_up"],
  "deployment scheduler ready" => ["ai_companion_deployment_scheduler_ready"],
  "deployment scheduler due" => ["ai_companion_deployment_scheduler_due"],
  "deployment scheduler failures" => [
    "ai_companion_deployment_scheduler_consecutive_failures"
  ],
  "deployment scheduler certification" => [
    "ai_companion_deployment_scheduler_certification_expiry_timestamp_seconds",
    "time()"
  ],
  "deployment shadow state" => [
    "ai_companion_deployment_shadow_up"
  ],
  "deployment shadow readiness" => [
    "ai_companion_deployment_shadow_ready"
  ],
  "deployment shadow outcomes" => [
    "ai_companion_deployment_shadow_outcomes"
  ],
  "deployment shadow drift" => [
    "ai_companion_deployment_shadow_drift"
  ],
  "deployment shadow lookup errors" => [
    "ai_companion_deployment_shadow_lookup_errors"
  ]
}.freeze

def duplicate_values(values)
  counts = values.each_with_object(Hash.new(0)) { |value, result| result[value] += 1 }
  counts.select { |_value, count| count > 1 }.keys
end

def validation_errors(rules_document, dashboard)
  errors = []
  groups = rules_document.is_a?(Hash) ? rules_document["groups"] : nil
  unless groups.is_a?(Array) && !groups.empty?
    errors << "Prometheus rules must contain at least one group"
    groups = []
  end
  alert_rules = groups.flat_map do |group|
    group.is_a?(Hash) && group["rules"].is_a?(Array) ? group["rules"] : []
  end.select { |rule| rule.is_a?(Hash) && rule["alert"].is_a?(String) }
  alert_names = alert_rules.map { |rule| rule["alert"] }
  duplicates = duplicate_values(alert_names)
  errors << "duplicate alert names: #{duplicates.join(', ')}" unless duplicates.empty?

  alert_rules.each do |rule|
    name = rule["alert"]
    errors << "#{name} must define a non-empty expression" unless rule["expr"].is_a?(String) && !rule["expr"].strip.empty?
    errors << "#{name} must define a bounded for duration" unless rule["for"].is_a?(String) && rule["for"].match?(/\A\d+[smhd]\z/)
    severity = rule.dig("labels", "severity")
    errors << "#{name} severity must be page or ticket" unless %w[page ticket].include?(severity)
    runbook = rule.dig("annotations", "runbook")
    errors << "#{name} must link a runbook" unless runbook.is_a?(String) && runbook.start_with?("docs/runbooks/")
  end

  REQUIRED_RETRY_ALERTS.each do |name, contract|
    rule = alert_rules.find { |candidate| candidate["alert"] == name }
    if rule.nil?
      errors << "missing required retry alert #{name}"
      next
    end
    errors << "#{name} severity must be #{contract['severity']}" unless rule.dig("labels", "severity") == contract["severity"]
    errors << "#{name} for duration must be #{contract['for']}" unless rule["for"] == contract["for"]
    expression = rule["expr"].to_s
    contract["fragments"].each do |fragment|
      errors << "#{name} expression is missing #{fragment}" unless expression.include?(fragment)
    end
  end
  REQUIRED_AGENT_DISPATCH_ALERTS.each do |name, contract|
    rule = alert_rules.find { |candidate| candidate["alert"] == name }
    if rule.nil?
      errors << "missing required Agent dispatch alert #{name}"
      next
    end
    errors << "#{name} severity must be #{contract['severity']}" unless rule.dig("labels", "severity") == contract["severity"]
    errors << "#{name} for duration must be #{contract['for']}" unless rule["for"] == contract["for"]
    expression = rule["expr"].to_s
    contract["fragments"].each do |fragment|
      errors << "#{name} expression is missing #{fragment}" unless expression.include?(fragment)
    end
  end
  REQUIRED_SCHEDULER_ALERTS.each do |name, contract|
    rule = alert_rules.find { |candidate| candidate["alert"] == name }
    if rule.nil?
      errors << "missing required scheduler alert #{name}"
      next
    end
    errors << "#{name} severity must be #{contract['severity']}" unless rule.dig("labels", "severity") == contract["severity"]
    errors << "#{name} for duration must be #{contract['for']}" unless rule["for"] == contract["for"]
    expression = rule["expr"].to_s
    contract["fragments"].each do |fragment|
      errors << "#{name} expression is missing #{fragment}" unless expression.include?(fragment)
    end
  end
  REQUIRED_SHADOW_ALERTS.each do |name, contract|
    rule = alert_rules.find { |candidate| candidate["alert"] == name }
    if rule.nil?
      errors << "missing required shadow alert #{name}"
      next
    end
    errors << "#{name} severity must be #{contract['severity']}" unless rule.dig("labels", "severity") == contract["severity"]
    errors << "#{name} for duration must be #{contract['for']}" unless rule["for"] == contract["for"]
    expression = rule["expr"].to_s
    contract["fragments"].each do |fragment|
      errors << "#{name} expression is missing #{fragment}" unless expression.include?(fragment)
    end
  end

  panels = dashboard.is_a?(Hash) ? dashboard["panels"] : nil
  unless panels.is_a?(Array) && !panels.empty?
    errors << "Grafana dashboard must contain panels"
    panels = []
  end
  panel_ids = panels.map { |panel| panel["id"] if panel.is_a?(Hash) }.compact
  duplicate_ids = duplicate_values(panel_ids)
  errors << "duplicate Grafana panel IDs: #{duplicate_ids.join(', ')}" unless duplicate_ids.empty?
  expressions = panels.flat_map do |panel|
    panel.is_a?(Hash) && panel["targets"].is_a?(Array) ? panel["targets"] : []
  end.map { |target| target["expr"] if target.is_a?(Hash) }.compact
  REQUIRED_DASHBOARD_EXPRESSIONS.each do |name, fragments|
    errors << "Grafana dashboard is missing #{name}" unless expressions.any? do |expression|
      fragments.all? { |fragment| expression.include?(fragment) }
    end
  end
  errors
end

def scrape_validation_errors(document)
  errors = []
  configs = document.is_a?(Hash) ? document["scrape_configs"] : nil
  unless configs.is_a?(Array)
    return ["Prometheus configuration must define scrape_configs"]
  end
  names = configs.each_with_object([]) do |config, result|
    result << config["job_name"] if config.is_a?(Hash) && config["job_name"]
  end
  duplicates = duplicate_values(names)
  errors << "duplicate Prometheus scrape jobs: #{duplicates.join(', ')}" unless duplicates.empty?
  REQUIRED_SCRAPE_TARGETS.each do |job_name, target|
    config = configs.find { |candidate| candidate.is_a?(Hash) && candidate["job_name"] == job_name }
    if config.nil?
      errors << "missing required Prometheus scrape job #{job_name}"
      next
    end
    targets = Array(config["static_configs"]).flat_map do |static_config|
      static_config.is_a?(Hash) ? Array(static_config["targets"]) : []
    end
    errors << "Prometheus scrape job #{job_name} must target #{target}" unless targets.include?(target)
  end
  errors
end

def load_documents
  rules = YAML.safe_load(File.read(RULES_PATH), aliases: false)
  dashboard = JSON.parse(File.read(DASHBOARD_PATH))
  prometheus = YAML.safe_load(File.read(PROMETHEUS_PATH), aliases: false)
  [rules, dashboard, prometheus]
end

rules, dashboard, prometheus = load_documents
errors = validation_errors(rules, dashboard) + scrape_validation_errors(prometheus)
unless errors.empty?
  warn errors.join("\n")
  exit 1
end

if ARGV.include?("--self-test")

  missing_agent_scrape = Marshal.load(Marshal.dump(prometheus))
  missing_agent_scrape["scrape_configs"].reject! { |config| config["job_name"] == "agent-worker" }
  unless scrape_validation_errors(missing_agent_scrape).any? { |error| error.include?("missing required Prometheus scrape job") }
    warn "self-test failed to reject a missing Agent Worker scrape job"
    exit 1
  end
  missing_alert = Marshal.load(Marshal.dump(rules))
  missing_alert["groups"].each do |group|
    group["rules"].reject! { |rule| rule["alert"] == "AICompanionAgentRetryStorm" }
  end

  missing_scheduler_alert = Marshal.load(Marshal.dump(rules))
  missing_scheduler_alert["groups"].each do |group|
    group["rules"].reject! { |rule| rule["alert"] == "AICompanionDeploymentSchedulerUnavailable" }
  end
  unless validation_errors(missing_scheduler_alert, dashboard).any? { |error| error.include?("missing required scheduler alert") }
    warn "self-test failed to reject a missing scheduler alert"
    exit 1
  end
  missing_shadow_alert = Marshal.load(Marshal.dump(rules))
  missing_shadow_alert["groups"].each do |group|
    group["rules"].reject! { |rule| rule["alert"] == "AICompanionDeploymentShadowUnavailable" }
  end
  unless validation_errors(missing_shadow_alert, dashboard).any? { |error| error.include?("missing required shadow alert") }
    warn "self-test failed to reject a missing shadow alert"
    exit 1
  end
  unless validation_errors(missing_alert, dashboard).any? { |error| error.include?("missing required retry alert") }
    warn "self-test failed to reject a missing retry alert"
    exit 1
  end

  missing_dispatch_alert = Marshal.load(Marshal.dump(rules))
  missing_dispatch_alert["groups"].each do |group|
    group["rules"].reject! { |rule| rule["alert"] == "AICompanionAgentToolWakeLatencyHigh" }
  end
  unless validation_errors(missing_dispatch_alert, dashboard).any? { |error| error.include?("missing required Agent dispatch alert") }
    warn "self-test failed to reject a missing Agent dispatch alert"
    exit 1
  end

  unsafe_ratio = Marshal.load(Marshal.dump(rules))
  unsafe_rule = unsafe_ratio["groups"].flat_map { |group| group["rules"] }.find do |rule|
    rule["alert"] == "AICompanionAgentRetryRecoveryLow"
  end
  unsafe_rule["expr"] = "ai_companion_agent_execution_retry_recovery_ratio < 0.8"
  unless validation_errors(unsafe_ratio, dashboard).any? { |error| error.include?(">= 5") }
    warn "self-test failed to reject a recovery alert without a minimum sample guard"
    exit 1
  end

  mismatched_ratio = Marshal.load(Marshal.dump(rules))
  mismatched_rule = mismatched_ratio["groups"].flat_map { |group| group["rules"] }.find do |rule|
    rule["alert"] == "AICompanionAgentRetryRecoveryLow"
  end
  mismatched_rule["expr"] = mismatched_rule["expr"].sub("and on()", "and")
  unless validation_errors(mismatched_ratio, dashboard).any? { |error| error.include?("and on()") }
    warn "self-test failed to reject a recovery alert with implicit label matching"
    exit 1
  end

  duplicate_panel = Marshal.load(Marshal.dump(dashboard))
  duplicate_panel["panels"] << Marshal.load(Marshal.dump(duplicate_panel["panels"].first))
  unless validation_errors(rules, duplicate_panel).any? { |error| error.include?("duplicate Grafana panel IDs") }
    warn "self-test failed to reject duplicate panel IDs"
    exit 1
  end

  unguarded_dashboard = Marshal.load(Marshal.dump(dashboard))
  recovery_target = unguarded_dashboard["panels"].flat_map { |panel| panel["targets"] || [] }.find do |target|
    target["expr"].to_s.include?("ai_companion_agent_execution_retry_recovery_ratio")
  end
  recovery_target["expr"] = "ai_companion_agent_execution_retry_recovery_ratio"
  unless validation_errors(rules, unguarded_dashboard).any? { |error| error.include?("sample-guarded retry recovery") }
    warn "self-test failed to reject an unguarded recovery dashboard panel"
    exit 1
  end


  mismatched_dashboard = Marshal.load(Marshal.dump(dashboard))
  recovery_target = mismatched_dashboard["panels"].flat_map { |panel| panel["targets"] || [] }.find do |target|
    target["expr"].to_s.include?("ai_companion_agent_execution_retry_recovery_ratio")
  end
  recovery_target["expr"] = recovery_target["expr"].sub("and on()", "and")
  unless validation_errors(rules, mismatched_dashboard).any? { |error| error.include?("sample-guarded retry recovery") }
    warn "self-test failed to reject a recovery dashboard with implicit label matching"
    exit 1
  end
  unsafe_wake_latency = Marshal.load(Marshal.dump(rules))
  wake_latency_rule = unsafe_wake_latency["groups"].flat_map { |group| group["rules"] }.find do |rule|
    rule["alert"] == "AICompanionAgentToolWakeLatencyHigh"
  end
  wake_latency_rule["expr"] = "histogram_quantile(0.95, sum by (le) (rate(ai_companion_agent_tool_wake_latency_seconds_bucket[5m]))) > 1"
  unless validation_errors(unsafe_wake_latency, dashboard).any? { |error| error.include?(">= 5") }
    warn "self-test failed to reject Agent wake latency without a minimum sample guard"
    exit 1
  end

  puts "observability_validator_self_test=passed"
else
  puts "observability_contract=valid retry_alerts=#{REQUIRED_RETRY_ALERTS.length} agent_dispatch_alerts=#{REQUIRED_AGENT_DISPATCH_ALERTS.length} scheduler_alerts=#{REQUIRED_SCHEDULER_ALERTS.length} shadow_alerts=#{REQUIRED_SHADOW_ALERTS.length} dashboard_metrics=#{REQUIRED_DASHBOARD_EXPRESSIONS.length} scrape_jobs=#{REQUIRED_SCRAPE_TARGETS.length}"
end
