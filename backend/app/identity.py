CURRENT_USER_ID = "dailing"


def normalize_user_id(_value: str | None = None) -> str:
    """Return the fixed persistence owner for the single-user Viewer."""
    return CURRENT_USER_ID
