import os
from functools import lru_cache
from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    app_env: str = "development"
    database_url: str = "/data/bastion.db"
    app_master_key: str = ""
    admin_username: str = "admin"
    admin_password_hash: str = ""
    panel_base_url: str = "http://localhost:8080"

    # Token settings
    admin_token_ttl_hours: int = 24
    enrollment_token_ttl_minutes: int = 30

    # Cloudflare (optional)
    cloudflare_account_id: str = ""
    cloudflare_api_token: str = ""

    # GitHub (optional)
    github_app_id: str = ""
    github_app_private_key: str = ""

    class Config:
        env_file = ".env"
        extra = "ignore"


@lru_cache()
def get_settings() -> Settings:
    return Settings()
