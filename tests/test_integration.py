# tests/test_integration.py - Comprehensive Integration Test Suite

import pytest
from fastapi.testclient import TestClient
from httpx import AsyncClient, ASGITransport
import asyncio
import time

from backend.main import app
from backend.db.database import SessionLocal, engine
from backend.domain.models import Base as DomainBase


@pytest.fixture(scope="session")
def db():
    """Create a new database session for testing."""
    # Create tables for tests
    DomainBase.metadata.create_all(bind=engine)
    yield SessionLocal()
    # Clean up after all tests complete
    DomainBase.metadata.drop_all(bind=engine)


@pytest.fixture
def client():
    """Create a test client for FastAPI app."""
    return TestClient(app)


class TestIntegration:
    def test_api_endpoint_integration(self, client):
        """
        Test complete workflow from API endpoint to database persistence.

        This test verifies that market data received via the API is properly stored
        and can be retrieved by subsequent requests.
        """
        # Step 1: Send market data through API endpoint
        response = client.post(
            "/api/market/data",
            json={"symbol": "BTC/USD", "price": 25000.50, "volume_24h": 1200000, "timestamp": "2026-04-27T12:00:00Z"},
        )

        # Verify successful response
        assert response.status_code == 200
        data = response.json()
        assert data["symbol"] == "BTC/USD"
        assert "id" in data
        assert "created_at" in data

        # Step 2: Retrieve the stored market data via API
        get_response = client.get(f"/api/market/data/{data['id']}")
        assert get_response.status_code == 200
        retrieved_data = get_response.json()

        # Verify all fields are preserved
        assert retrieved_data["symbol"] == "BTC/USD"
        assert abs(retrieved_data["price"] - 25000.50) < 0.01

    @pytest.mark.asyncio
    async def test_llm_integration(self, db):
        """
        Test LLM analysis integration with mock market data.

        This verifies that the AI analysis workflow properly handles:
        - Market data processing
        - LLM inference
        - Result storage and retrieval
        """
        from backend.llm.analyser import LLMAnalyser

        # Initialize LLM analyser with test database
        analyser = LLMAnalyser(db)

        # Mock market data that would come from the exchange API
        market_data = {
            "symbol": "ETH/USD",
            "price": 1800.50,
            "volume_24h": 1000000,
            "trends": ["bullish", "strong"],
            "timestamp": "2026-04-27T12:00:00Z",
        }

        # Step 1: Analyze market data using LLM
        analysis = await analyser.analyze_market(market_data)

        # Verify successful analysis
        assert analysis is not None, "LLM should return a valid analysis object"
        assert hasattr(analysis, "signal"), "Analysis should contain trading signal"
        assert hasattr(analysis, "confidence"), "Analysis should contain confidence score"
        assert analysis.confidence > 0.5, f"Confidence too low: {analysis.confidence}"

        # Step 2: Verify the analysis was stored in database
        from backend.db.repository import get_analysis_by_id

        db_analysis = await get_analysis_by_id(db, analysis.id)
        assert db_analysis is not None, "Analysis should be persisted to database"
        assert db_analysis.signal == analysis.signal
        assert abs(db_analysis.confidence - analysis.confidence) < 0.001

    def test_database_api_interaction(self, client):
        """
        Test complete workflow from portfolio metric submission to API retrieval.

        This verifies the entire pipeline:
        - Portfolio metrics received via API
        - Metrics stored in database
        - Metrics retrievable through API endpoints
        """
        # Step 1: Submit portfolio metrics via API
        response = client.post(
            "/api/portfolio/metrics",
            json={
                "symbol": "ADA/USD",
                "position_size": 2.5,
                "entry_price": 0.75,
                "stop_loss": 0.65,
                "take_profit": 1.00,
                "timestamp": "2026-04-27T12:00:00Z",
            },
        )

        # Verify successful response
        assert response.status_code == 200, f"Expected 200 status, got {response.status_code}"
        data = response.json()
        assert data["symbol"] == "ADA/USD"
        assert "id" in data

        # Step 2: Retrieve metrics using the API
        metric_id = data["id"]
        get_response = client.get(f"/api/portfolio/metrics/{metric_id}")

        # Verify retrieved metrics
        assert get_response.status_code == 200
        retrieved_data = get_response.json()

        assert retrieved_data["symbol"] == "ADA/USD"
        assert abs(retrieved_data["position_size"] - 2.5) < 0.001
        assert abs(retrieved_data["entry_price"] - 0.75) < 0.001

    @pytest.mark.performance
    def test_api_performance(self, client):
        """
        Test API performance under load.

        Ensures the system can handle realistic request volumes without timing out.
        """
        # Measure response time for multiple requests
        start_time = time.time()

        # Make 100 concurrent requests to /api/market/data endpoint
        async def make_request():
            return client.get("/api/market/data?symbol=BTC/USD")

        async def run_concurrent_requests():
            async with AsyncClient(transport=ASGITransport(app=app), base_url="http://testserver") as ac:
                tasks = [make_request() for _ in range(100)]
                return await asyncio.gather(*tasks)

        responses = asyncio.run(run_concurrent_requests())

        # Verify all requests succeeded
        assert all(r.status_code == 200 for r in responses), "All concurrent requests should succeed"

        # Calculate and verify performance metrics
        duration = time.time() - start_time
        print(f"Completed 100 API requests in {duration:.2f}s")

        # Ensure average response time is under 50ms (0.05s)
        total_time = sum(r.elapsed.total_seconds() for r in responses if hasattr(r, "elapsed"))
        avg_time = total_time / len(responses) if responses else 0
        assert avg_time < 0.05, f"Average response time {avg_time}s exceeds limit of 50ms"

    def test_error_handling(self, client):
        """
        Test API error handling for invalid inputs.

        Verifies that the system gracefully handles malformed requests and returns
        appropriate error messages with correct status codes.
        """
        # Test missing required fields in trade signal request
        response = client.post("/api/trade/signal", json={})
        assert response.status_code == 422, "Missing required fields should return 422 Unprocessable Entity"

        data = response.json()
        assert "detail" in data, "Error response should contain detail array"
        assert len(data["detail"]) > 0, "Error details should not be empty"

        # Test invalid symbol format
        response = client.post("/api/trade/signal", json={"symbol": "INVALID_SYMBOL_FORMAT", "indicators": {}})
        assert response.status_code == 422, "Invalid symbol format should return 422 Unprocessable Entity"

    def test_workflow_integration(self, client):
        """
        Test complete trading workflow integration.

        Simulates the entire trading cycle:
        1. Receive market data
        2. Generate trade signal via LLM
        3. Execute trade
        4. Update portfolio metrics
        """
        # Step 1: Receive market data
        market_data_response = client.post(
            "/api/market/data",
            json={"symbol": "SOL/USD", "price": 85.50, "volume_24h": 500000, "timestamp": "2026-04-27T12:00:00Z"},
        )

        assert market_data_response.status_code == 200

        # Step 2: Get trade signal from LLM
        signal_response = client.post(
            "/api/trade/signal",
            json={"symbol": "SOL/USD", "indicators": {"rsi": 45.5, "macd": -0.8, "moving_average": 82.3}},
        )

        assert signal_response.status_code == 200, "Trade signal generation should succeed"
        signal_data = signal_response.json()
        assert "signal" in signal_data

        # Step 3: Execute trade (mocked)
        execution_response = client.post(
            "/api/trade/execute", json={"symbol": "SOL/USD", "action": "buy", "quantity": 1.5, "price": 85.50}
        )

        assert execution_response.status_code == 200, "Trade execution should succeed"

        # Step 4: Update portfolio metrics
        metrics_response = client.post(
            "/api/portfolio/metrics",
            json={
                "symbol": "SOL/USD",
                "position_size": 1.5,
                "entry_price": 85.50,
                "stop_loss": 80.00,
                "take_profit": 100.00,
            },
        )

        assert metrics_response.status_code == 200, "Portfolio metric update should succeed"
