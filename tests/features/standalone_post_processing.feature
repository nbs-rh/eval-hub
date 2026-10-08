@evaluations
@post_processing
Feature: Post-Processing Endpoint
  As a data scientist
  I want to submit post-processing computations against evaluation results
  So that I can analyze completed evaluation jobs

  Background:
    Given I set the header "X-Tenant" to "{{env:X_TENANT|test-tenant}}"
    And I set the header "X-User" to "{{env:X_USER|test-user}}"

  @local
  Scenario: Create post-processing computation from a completed evaluation job
    Given the service is running
    When I send a POST request to "/api/v1/evaluations/jobs" with body "file:/evaluation_job.json"
    Then the response code should be 202
    And the "resource.id" field in the response should be saved as "value:source_job_id"
    When I send a POST request to "/api/v1/evaluations/jobs/{id}/events" with body "file:/evaluation_job_status_event_running.json"
    Then the response code should be 204
    When I send a POST request to "/api/v1/evaluations/jobs/{id}/events" with body:
    """
    {
      "benchmark_status_event": {
        "id": "arc_easy",
        "provider_id": "lm_evaluation_harness",
        "status": "completed",
        "metrics": {"accuracy": 0.7},
        "metrics_schema": [{"name": "accuracy", "type": "numeric"}]
      }
    }
    """
    Then the response code should be 204
    When I send a GET request to "/api/v1/evaluations/jobs/{id}"
    Then the response code should be 200
    And the response should contain the value "completed" at path "$.status.state"
    And the response should contain the value "0.7" at path "$.results.benchmarks[0].metrics.accuracy"
    When I send a POST request to "/api/v1/evaluations/post-processing" with body:
    """
    {
      "name": "confidence-interval-fvt",
      "operations": {
        "confidence_interval": {
          "results_data_ref": {
            "eval_job": {
              "id": "{{value:source_job_id}}"
            }
          },
          "calibration_data_ref": [
            {
              "pvc": {"claim_name": "calibration-data"},
              "data_config": {
                "format": "jsonl",
                "columns": {
                  "label": "label",
                  "prediction": "prediction"
                }
              }
            }
          ],
          "significance_level": 0.05
        }
      }
    }
    """
    Then the response code should be 202
    And the response should contain the value "confidence-interval-fvt" at path "$.name"
    And the response should contain the value "pending" at path "$.status.state"
    And the response should contain the value "{{value:source_job_id}}" at path "$.operations.confidence_interval.results_data_ref.eval_job.id"
    And the "resource.id" field in the response should be saved as "value:post_processing_id"

  # This cluster E2E matrix seeds the selected stores for each row. Git fixtures
  # are checked into tests/git-testdata/post_processing; S3, PVC, MLflow, OCI,
  # and Hugging Face fixtures are uploaded by the preparation step.
  @cluster
  @mlflow
  @post_processing_e2e
  Scenario Outline: Run standalone post-processing across framework artifact layouts
    Given the service is running
    And I set the wait deadline to "20m"
    And I set the wait interval to "5s"
    And I prepare standalone post-processing fixtures for "<framework>" using results source "<results_source>" and calibration source "<calibration_source>"
    When I submit the prepared standalone post-processing request
    Then the response code should be 202
    And the response should contain the value "accuracy" at path "$.operations.confidence_interval.primary_score.metric"
    And the "resource.id" field in the response should be saved as "value:post_processing_id"
    When I wait for standalone post-processing "value:post_processing_id" to reach "completed"
    Then the response code should be 200
    And the response should contain the value "completed" at path "$.status.state"
    And the response should contain a valid confidence interval

    Examples:
      | framework       | results_source | calibration_source |
      | Inspect         | S3             | S3                 |
      | Inspect         | PVC            | PVC                |
      | Inspect         | Git            | Git                |
      | Inspect         | HF             | HF                 |
      | Inspect         | MLFlow         | PVC                |
      | Inspect         | OCI            | S3                 |
      | LightEval       | S3             | PVC                |
      | LightEval       | PVC            | Git                |
      | LightEval       | Git            | HF                 |
      | LightEval       | HF             | S3                 |
      | LightEval       | MLFlow         | Git                |
      | LightEval       | OCI            | HF                 |
      | RAGAS           | S3             | Git                |
      | RAGAS           | PVC            | HF                 |
      | RAGAS           | Git            | S3                 |
      | RAGAS           | HF             | PVC                |
      | RAGAS           | MLFlow         | HF                 |
      | RAGAS           | OCI            | Git                |
      | RULER           | S3             | HF                 |
      | RULER           | PVC            | S3                 |
      | RULER           | Git            | PVC                |
      | RULER           | HF             | Git                |
      | RULER           | MLFlow         | S3                 |
      | RULER           | OCI            | PVC                |
      | Clear           | MLFlow         | S3                 |
      | Clear           | OCI            | PVC                |
      | DeepEval        | MLFlow         | Git                |
      | DeepEval        | OCI            | HF                 |
      | GuideLLM        | OCI            | S3                 |
      | MTEB            | OCI            | PVC                |
      | NeMo Guardrails | MLFlow         | Git                |
      | SWE-bench       | MLFlow         | HF                 |
      | SWE-bench       | OCI            | S3                 |
