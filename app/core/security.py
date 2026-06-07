import secrets
import hashlib
import hmac
import base64
from datetime import datetime, timedelta, timezone
from typing import Optional

from passlib.context import CryptContext
from jose import jwt, JWTError

from app.core.config import get_settings

pwd_context = CryptContext(schemes=["bcrypt"], deprecated="auto")


def verify_password(plain_password: str, hashed_password: str) -> bool:
    return pwd_context.verify(plain_password, hashed_password)


def get_password_hash(password: str) -> str:
    return pwd_context.hash(password)


def create_admin_token(data: dict, expires_delta: Optional[timedelta] = None) -> str:
    settings = get_settings()
    to_encode = data.copy()
    if expires_delta:
        expire = datetime.now(timezone.utc) + expires_delta
    else:
        expire = datetime.now(timezone.utc) + timedelta(hours=settings.admin_token_ttl_hours)
    to_encode.update({"exp": expire, "type": "admin"})
    return jwt.encode(to_encode, settings.app_master_key, algorithm="HS256")


def decode_admin_token(token: str) -> Optional[dict]:
    settings = get_settings()
    try:
        payload = jwt.decode(token, settings.app_master_key, algorithms=["HS256"])
        if payload.get("type") != "admin":
            return None
        return payload
    except JWTError:
        return None


def generate_enrollment_token() -> str:
    return "enroll_" + secrets.token_urlsafe(32)


def generate_node_token() -> str:
    return "node_" + secrets.token_urlsafe(32)


def hash_token(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()


def derive_key(master_key: str, salt: str, length: int = 32) -> bytes:
    return hashlib.pbkdf2_hmac("sha256", master_key.encode(), salt.encode(), 100000, dklen=length)


def encrypt_payload(master_key: str, plaintext: str) -> str:
    import os
    from cryptography.fernet import Fernet
    salt = os.urandom(16).hex()
    key = derive_key(master_key, salt, length=32)
    fernet_key = base64.urlsafe_b64encode(key)
    f = Fernet(fernet_key)
    ciphertext = f.encrypt(plaintext.encode()).decode()
    return f"{salt}:{ciphertext}"


def decrypt_payload(master_key: str, payload: str) -> str:
    from cryptography.fernet import Fernet
    if ":" not in payload:
        return payload
    salt, ciphertext = payload.split(":", 1)
    key = derive_key(master_key, salt, length=32)
    fernet_key = base64.urlsafe_b64encode(key)
    f = Fernet(fernet_key)
    return f.decrypt(ciphertext.encode()).decode()
