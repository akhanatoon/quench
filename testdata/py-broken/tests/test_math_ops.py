from app.math_ops import multiply


def test_multiply():
    got = multiply(3, 4)
    assert got == 12, f"expected 12, got {got}"
